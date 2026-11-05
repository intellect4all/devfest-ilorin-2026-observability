// Command ledger owns balances and double-entry postings. It is already
// instrumented: otelhttp extracts the caller's traceparent, so once the
// transfers service propagates context, ledger spans join its trace.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"devfest-observability/internal/obs"
)

var tracer = otel.Tracer("ledger")

const (
	balanceQuery = `SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = $1`
	insertEntry  = `INSERT INTO entries (account_id, transfer_id, amount) VALUES ($1, $2, $3)`
)

var schema = []string{
	`CREATE TABLE IF NOT EXISTS entries (
		id          BIGSERIAL PRIMARY KEY,
		account_id  TEXT        NOT NULL,
		transfer_id TEXT        NOT NULL,
		amount      BIGINT      NOT NULL,
		memo        TEXT        NOT NULL DEFAULT '',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_entries_account_id ON entries (account_id)`,
}

type server struct {
	db  *pgxpool.Pool
	log *slog.Logger
}

func main() {
	ctx := context.Background()
	log := obs.NewLogger("ledger")

	shutdown, err := obs.InitTracer(ctx, "ledger")
	if err != nil {
		log.Error("tracer init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	db, err := pgxpool.New(ctx, obs.Env("DATABASE_URL", "postgres://devfest:devfest@localhost:5432/devfest"))
	if err != nil {
		log.Error("database pool failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	for _, stmt := range schema {
		if _, err := db.Exec(ctx, stmt); err != nil {
			log.Error("migration failed", "err", err)
			os.Exit(1)
		}
	}

	s := &server{db: db, log: log}
	mux := http.NewServeMux()
	mux.Handle("GET /balance", obs.RED("ledger", "GET /balance", http.HandlerFunc(s.balance)))
	mux.Handle("POST /entries", obs.RED("ledger", "POST /entries", http.HandlerFunc(s.postEntries)))
	mux.HandleFunc("POST /admin/fault", s.fault)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", obs.Healthz)

	handler := otelhttp.NewHandler(mux, "ledger",
		otelhttp.WithSpanNameFormatter(obs.SpanName),
		otelhttp.WithFilter(obs.SkipNoise),
	)
	obs.Serve(log, ":"+obs.Env("PORT", "8081"), handler)
}

// balance answers GET /balance?account=acct-00001.
func (s *server) balance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := r.URL.Query().Get("account")
	if account == "" {
		obs.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "account is required"})
		return
	}

	bal, err := s.queryBalance(ctx, account)
	if err != nil {
		s.fail(ctx, w, "balance query failed", err)
		return
	}
	// Note what this log line does NOT say: how long the query took.
	s.log.InfoContext(ctx, "balance checked", "account", account)
	obs.WriteJSON(w, http.StatusOK, map[string]any{"account": account, "balance": bal})
}

func (s *server) queryBalance(ctx context.Context, account string) (int64, error) {
	ctx, span := tracer.Start(ctx, "ledger.balance_query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.query.text", balanceQuery),
		),
	)
	defer span.End()

	var bal int64
	if err := s.db.QueryRow(ctx, balanceQuery, account).Scan(&bal); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "balance query failed")
		return 0, err
	}
	return bal, nil
}

type entryRequest struct {
	TransferID string `json:"transfer_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Amount     int64  `json:"amount"`
}

// postEntries writes one debit and one credit in a single transaction.
func (s *server) postEntries(w http.ResponseWriter, r *http.Request) {
	var req entryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TransferID == "" || req.Amount <= 0 {
		obs.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "transfer_id, from, to and a positive amount are required"})
		return
	}

	ctx, span := tracer.Start(r.Context(), "ledger.post_entries",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.query.text", insertEntry),
			attribute.String("transfer.id", req.TransferID),
		),
	)
	defer span.End()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.failSpan(ctx, w, span, "begin failed", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	if _, err := tx.Exec(ctx, insertEntry, req.From, req.TransferID, -req.Amount); err != nil {
		s.failSpan(ctx, w, span, "debit failed", err)
		return
	}
	if _, err := tx.Exec(ctx, insertEntry, req.To, req.TransferID, req.Amount); err != nil {
		s.failSpan(ctx, w, span, "credit failed", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.failSpan(ctx, w, span, "commit failed", err)
		return
	}

	s.log.InfoContext(ctx, "entries posted",
		"transfer_id", req.TransferID, "from", req.From, "to", req.To, "amount", req.Amount)
	obs.WriteJSON(w, http.StatusCreated, map[string]string{"transfer_id": req.TransferID})
}

// fault is the presenter's switch for the workshop bug:
// POST /admin/fault?slow_balance=on drops the index the balance query needs.
// It is excluded from traces and metrics so it doesn't give the game away.
func (s *server) fault(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("slow_balance")
	var stmt string
	switch mode {
	case "on":
		stmt = `DROP INDEX IF EXISTS idx_entries_account_id`
	case "off":
		stmt = `CREATE INDEX IF NOT EXISTS idx_entries_account_id ON entries (account_id)`
	default:
		obs.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "slow_balance must be on or off"})
		return
	}
	if _, err := s.db.Exec(r.Context(), stmt); err != nil {
		obs.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	obs.WriteJSON(w, http.StatusOK, map[string]string{"slow_balance": mode})
}

func (s *server) fail(ctx context.Context, w http.ResponseWriter, msg string, err error) {
	s.log.ErrorContext(ctx, msg, "err", err)
	obs.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
}

func (s *server) failSpan(ctx context.Context, w http.ResponseWriter, span trace.Span, msg string, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, msg)
	s.fail(ctx, w, msg, err)
}
