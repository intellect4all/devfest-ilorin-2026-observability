// Command transfers is the public payments API. POST /transfers moves money
// between two accounts: it checks for a duplicate request, counts the amount
// against the sender's daily limit, asks the ledger for the balance, posts
// the ledger entries and pays out through the provider.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"devfest/internal/httpx"
)

type app struct {
	store    *store
	recent   *recentTransfers
	locks    *accountLocks
	ledger   *ledgerClient
	provider *providerClient

	prefunded map[string]bool // settlement wallets that skip the balance check
}

func main() {
	ctx := context.Background()

	db, err := pgxpool.New(ctx, httpx.Env("DATABASE_URL", "postgres://devfest:devfest@localhost:5432/devfest"))
	if err != nil {
		log.Fatalf("database pool: %v", err)
	}
	defer db.Close()

	limit, _ := strconv.ParseInt(httpx.Env("DAILY_LIMIT_KOBO", "100000000000000"), 10, 64)
	a := &app{
		store:     &store{db: db},
		recent:    newRecentTransfers(),
		locks:     newAccountLocks(limit),
		ledger:    newLedgerClient(httpx.Env("LEDGER_URL", "http://localhost:8081")),
		provider:  newProviderClient(httpx.Env("PROVIDER_URL", "http://localhost:8082")),
		prefunded: map[string]bool{},
	}
	// Fail closed: no account skips the balance check unless configured.
	for _, acct := range strings.Split(os.Getenv("PREFUNDED_ACCOUNTS"), ",") {
		if acct = strings.TrimSpace(acct); acct != "" {
			a.prefunded[acct] = true
		}
	}
	if err := a.store.migrate(ctx); err != nil {
		log.Fatalf("migration: %v", err)
	}

	// Remember the last 24 hours of transfers so retries are recognised
	// even after a restart.
	start := time.Now()
	recent, err := a.store.recent(ctx, time.Now().Add(-recentWindow))
	if err != nil {
		log.Fatalf("loading recent transfers: %v", err)
	}
	a.recent.Load(recent)
	log.Printf("loaded %d recent transfers in %s", len(recent), time.Since(start).Round(time.Millisecond))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /transfers", a.createTransfer)
	mux.HandleFunc("GET /healthz", httpx.Healthz)

	httpx.Serve(":"+httpx.Env("PORT", "8080"), mux, ":"+httpx.Env("ADMIN_PORT", "7080"), a.adminMux())
}

// adminMux serves the presenter's bug toggles:
//
//	GET  /admin/bugs            current state
//	POST /admin/bugs?a=on&c=off bug A: O(n) duplicate check; bug C: lock held across payout
func (a *app) adminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/bugs", a.bugState)
	mux.HandleFunc("POST /admin/bugs", func(w http.ResponseWriter, r *http.Request) {
		if on, ok := httpx.Toggle(r, "a"); ok {
			a.recent.linearScan.Store(on)
		}
		if on, ok := httpx.Toggle(r, "c"); ok {
			a.locks.holdAcrossPayout.Store(on)
		}
		a.bugState(w, r)
	})
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	return mux
}

func (a *app) bugState(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"a":                a.recent.linearScan.Load(),
		"c":                a.locks.holdAcrossPayout.Load(),
		"recent_transfers": a.recent.Len(),
	})
}
