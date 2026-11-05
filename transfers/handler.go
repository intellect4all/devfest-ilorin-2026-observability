package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"devfest/internal/httpx"
)

type transfer struct {
	ID          string    `json:"transfer_id"`
	Key         string    `json:"-"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Amount      int64     `json:"amount"` // kobo
	Status      string    `json:"status"`
	ProviderRef string    `json:"provider_ref,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (t *transfer) snapshot() *transfer { c := *t; return &c }

const (
	statusProcessing   = "processing"
	statusCompleted    = "completed"
	statusFailed       = "failed"
	statusInsufficient = "insufficient_funds"
)

// createTransfer handles POST /transfers {from, to, amount}.
func (a *app) createTransfer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Amount int64  `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "body must be JSON: {from, to, amount}")
		return
	}
	if req.From == "" || req.To == "" || req.From == req.To || req.Amount <= 0 {
		httpx.Error(w, http.StatusBadRequest, "from and to must differ and amount must be positive")
		return
	}

	t := &transfer{
		ID: newID(), Key: r.Header.Get("Idempotency-Key"),
		From: req.From, To: req.To, Amount: req.Amount,
		Status: statusProcessing, CreatedAt: time.Now().UTC(),
	}
	if t.Key == "" {
		t.Key = "auto-" + t.ID
	}

	// 1. Have we seen this Idempotency-Key before?
	var prev *transfer
	_ = step(ctx, "duplicate_check", func(ctx context.Context) error {
		prev, _ = a.recent.Claim(t)
		return nil
	})
	if prev != nil {
		a.replay(ctx, w, prev)
		return
	}

	// 2. Count the amount against the sender's daily limit.
	var res *reservation
	err := step(ctx, "account_lock.wait", func(ctx context.Context) error {
		var err error
		res, _, err = a.locks.Reserve(t.From, t.ID, t.Amount)
		return err
	})
	if errors.Is(err, errVelocityLimit) {
		countTransfer(ctx, "limited")
		a.recent.Forget(t)
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// 3. Move the money. From here on a client hanging up must not stop us
	// halfway through, so the work no longer follows the request's
	// cancellation (it still carries the request's values).
	ctx = context.WithoutCancel(ctx)
	out, err := a.move(ctx, t, res)
	res.Done(out.status != statusCompleted)
	a.recent.Complete(t, out.status, out.providerRef)
	if err != nil {
		log.Printf("transfer %s failed: %v", t.ID, err)
	}

	t = t.done(out)
	if err := a.store.save(ctx, t); err != nil {
		log.Printf("saving transfer %s: %v", t.ID, err)
		httpx.Error(w, http.StatusInternalServerError, "saving transfer failed")
		return
	}
	countTransfer(ctx, t.Status)
	log.Printf("transfer %s %s -> %s %d: %s", t.ID, t.From, t.To, t.Amount, t.Status)
	httpx.WriteJSON(w, out.code, t)
}

// outcome is how a transfer ended.
type outcome struct {
	status, providerRef string
	code                int // HTTP status for the client
}

// done returns a copy of t with the outcome applied. t itself is shared
// with other requests through recentTransfers, so it is not modified here.
func (t *transfer) done(o outcome) *transfer {
	c := *t
	c.Status, c.ProviderRef = o.status, o.providerRef
	return &c
}

// move checks the balance, posts the ledger entries and pays out.
func (a *app) move(ctx context.Context, t *transfer, res *reservation) (outcome, error) {
	// Pre-funded settlement wallets are topped up out of band; checking
	// their balance on every payout would only add load to the ledger.
	if !a.prefunded[t.From] {
		var balance int64
		res.Step("balance_check")
		err := step(ctx, "balance_check", func(ctx context.Context) error {
			var err error
			balance, err = a.ledger.Balance(ctx, t.From)
			return err
		})
		if err != nil {
			return outcome{statusFailed, "", http.StatusBadGateway}, err
		}
		if balance < t.Amount {
			return outcome{statusInsufficient, "", http.StatusUnprocessableEntity}, nil
		}
	}

	res.Step("post_entries")
	err := step(ctx, "post_entries", func(ctx context.Context) error {
		return a.ledger.PostEntries(ctx, t.ID, t.From, t.To, t.Amount)
	})
	if err != nil {
		return outcome{statusFailed, "", http.StatusBadGateway}, err
	}

	var ref string
	res.Step("payout")
	err = step(ctx, "payout", func(ctx context.Context) error {
		var err error
		ref, err = a.provider.Payout(ctx, t.ID, t.To, t.Amount)
		return err
	})
	if err != nil {
		// The money left the sender in the ledger but never arrived: reverse it.
		if rerr := a.ledger.PostEntries(ctx, t.ID+"-reversal", t.To, t.From, t.Amount); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return outcome{statusFailed, "", http.StatusBadGateway}, err
	}
	return outcome{statusCompleted, ref, http.StatusCreated}, nil
}

// replay answers a retried request with the transfer it already made.
func (a *app) replay(ctx context.Context, w http.ResponseWriter, prev *transfer) {
	countTransfer(ctx, "replayed")
	if prev.Status == statusProcessing {
		httpx.Error(w, http.StatusConflict, "a transfer with this Idempotency-Key is still in progress")
		return
	}
	log.Printf("idempotent replay of transfer %s", prev.ID)
	httpx.WriteJSON(w, http.StatusOK, prev)
}

func countTransfer(ctx context.Context, status string) {
	transfersProcessed.Add(ctx, 1, metric.WithAttributes(attribute.String("status", status)))
}

// step runs one business step of a transfer.
func step(ctx context.Context, name string, fn func(context.Context) error) error {
	return fn(ctx)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "trf_" + hex.EncodeToString(b)
}
