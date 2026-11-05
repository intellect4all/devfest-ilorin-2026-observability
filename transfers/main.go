// Command transfers is the public API: POST /transfers moves money between
// two accounts by checking the balance in the ledger, posting entries, and
// paying out through the provider. Attendees add tracing to this service.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"devfest-observability/internal/obs"
)

type app struct {
	db       *pgxpool.Pool
	log      *slog.Logger
	ledger   *ledgerClient
	provider *providerClient
}

func main() {
	ctx := context.Background()
	log := obs.NewLogger("transfers")

	// >>> STEP1
	shutdown, err := initTracer(ctx)
	if err != nil {
		log.Error("tracer init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()
	// <<< STEP1

	db, err := pgxpool.New(ctx, obs.Env("DATABASE_URL", "postgres://devfest:devfest@localhost:5432/devfest"))
	if err != nil {
		log.Error("database pool failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		log.Error("migration failed", "err", err)
		os.Exit(1)
	}

	a := &app{
		db:       db,
		log:      log,
		ledger:   newLedgerClient(obs.Env("LEDGER_URL", "http://localhost:8081")),
		provider: newProviderClient(obs.Env("PROVIDER_URL", "http://localhost:8082")),
	}

	mux := http.NewServeMux()
	mux.Handle("POST /transfers", obs.RED("transfers", "POST /transfers", http.HandlerFunc(a.createTransfer)))
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", obs.Healthz)

	// STEP1-ALT: handler := http.Handler(mux)
	// >>> STEP1
	handler := otelhttp.NewHandler(mux, "transfers",
		otelhttp.WithSpanNameFormatter(obs.SpanName),
		otelhttp.WithFilter(obs.SkipNoise),
	)
	// <<< STEP1

	obs.Serve(log, ":"+obs.Env("PORT", "8080"), handler)
}
