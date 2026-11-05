// Command ledger owns balances and double-entry postings. A balance is the
// sum of an account's entries; a transfer posts one debit and one credit in
// a single database transaction.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"devfest/internal/httpx"
)

const (
	balanceQuery = `SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = $1`
	insertEntry  = `INSERT INTO entries (account_id, transfer_id, amount) VALUES ($1, $2, $3)`
)

// migrations run once each, in order, and are recorded in schema_migrations.
var migrations = []string{
	`CREATE TABLE entries (
		id          BIGSERIAL PRIMARY KEY,
		account_id  TEXT        NOT NULL,
		transfer_id TEXT        NOT NULL,
		amount      BIGINT      NOT NULL,
		memo        TEXT        NOT NULL DEFAULT '',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	CREATE INDEX idx_entries_account_id ON entries (account_id)`,
}

type server struct{ db *pgxpool.Pool }

func main() {
	ctx := context.Background()

	db, err := pgxpool.New(ctx, httpx.Env("DATABASE_URL", "postgres://devfest:devfest@localhost:5432/devfest"))
	if err != nil {
		log.Fatalf("database pool: %v", err)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		log.Fatalf("migration: %v", err)
	}

	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /balance", s.balance)
	mux.HandleFunc("POST /entries", s.postEntries)
	mux.HandleFunc("GET /healthz", httpx.Healthz)

	httpx.Serve(":"+httpx.Env("PORT", "8081"), mux, ":"+httpx.Env("ADMIN_PORT", "7081"), s.adminMux())
}

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INT PRIMARY KEY)`); err != nil {
		return err
	}
	for i, stmt := range migrations {
		version := i + 1
		var done bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		if _, err := db.Exec(ctx, stmt); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return err
		}
		log.Printf("applied migration %d", version)
	}
	return nil
}

// balance answers GET /balance?account=acct-00001.
func (s *server) balance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := r.URL.Query().Get("account")
	if account == "" {
		httpx.Error(w, http.StatusBadRequest, "account is required")
		return
	}

	var bal int64
	if err := s.db.QueryRow(ctx, balanceQuery, account).Scan(&bal); err != nil {
		log.Printf("balance query for %s failed: %v", account, err)
		httpx.Error(w, http.StatusInternalServerError, "balance query failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": account, "balance": bal})
}

// postEntries writes one debit and one credit in a single transaction.
func (s *server) postEntries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		TransferID string `json:"transfer_id"`
		From       string `json:"from"`
		To         string `json:"to"`
		Amount     int64  `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TransferID == "" || req.From == "" || req.To == "" || req.Amount <= 0 {
		httpx.Error(w, http.StatusBadRequest, "transfer_id, from, to and a positive amount are required")
		return
	}

	err := func() error {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit
		if _, err := tx.Exec(ctx, insertEntry, req.From, req.TransferID, -req.Amount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, insertEntry, req.To, req.TransferID, req.Amount); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		log.Printf("posting entries for %s failed: %v", req.TransferID, err)
		httpx.Error(w, http.StatusInternalServerError, "posting entries failed")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"transfer_id": req.TransferID})
}

// adminMux serves the presenter's bug toggle:
//
//	GET  /admin/bugs       current state
//	POST /admin/bugs?b=on  bug B: drop every index on entries(account_id ...)
//	POST /admin/bugs?b=off put idx_entries_account_id back
//
// Bug B lives in the database, so it survives service restarts.
func (s *server) adminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/bugs", s.bugState)
	mux.HandleFunc("POST /admin/bugs", func(w http.ResponseWriter, r *http.Request) {
		if on, ok := httpx.Toggle(r, "b"); ok {
			var err error
			if on {
				err = s.dropAccountIndexes(r.Context())
			} else {
				// CONCURRENTLY, so live traffic keeps writing while it builds.
				_, err = s.db.Exec(r.Context(), `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_entries_account_id ON entries (account_id)`)
			}
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		s.bugState(w, r)
	})
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	return mux
}

// accountIndexes lists the indexes that can serve WHERE account_id = $1.
const accountIndexes = `SELECT indexname FROM pg_indexes
	WHERE tablename = 'entries' AND indexdef LIKE '%(account_id%'`

func (s *server) dropAccountIndexes(ctx context.Context) error {
	rows, err := s.db.Query(ctx, accountIndexes)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := s.db.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+pgx.Identifier{name}.Sanitize()); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) bugState(w http.ResponseWriter, r *http.Request) {
	var indexed bool
	err := s.db.QueryRow(r.Context(), `SELECT EXISTS (`+accountIndexes+`)`).Scan(&indexed)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"b": !indexed})
}
