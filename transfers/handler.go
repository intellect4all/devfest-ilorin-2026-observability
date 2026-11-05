package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"devfest-observability/internal/obs"
)

const maxAttempts = 3

type transferRequest struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"` // kobo
}

// createTransfer handles POST /transfers.
func (a *app) createTransfer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req transferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		obs.WriteJSON(w, http.StatusBadRequest, errBody("body must be JSON: {from, to, amount}"))
		return
	}
	if req.From == "" || req.To == "" || req.From == req.To || req.Amount <= 0 {
		obs.WriteJSON(w, http.StatusBadRequest, errBody("from and to must differ and amount must be positive"))
		return
	}

	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		id, status, found, err := a.findByIdempotencyKey(ctx, key)
		if err != nil {
			a.fail(ctx, w, http.StatusInternalServerError, "idempotency lookup failed", err)
			return
		}
		if found {
			a.log.InfoContext(ctx, "idempotent replay", "transfer_id", id)
			obs.WriteJSON(w, http.StatusOK, map[string]string{"transfer_id": id, "status": status})
			return
		}
	}

	transferID := newID()
	log := a.log.With("transfer_id", transferID)

	balance, err := a.ledger.Balance(ctx, req.From)
	if err != nil {
		a.fail(ctx, w, http.StatusBadGateway, "ledger balance check failed", err)
		return
	}
	if balance < req.Amount {
		log.InfoContext(ctx, "insufficient funds", "from", req.From, "amount", req.Amount)
		obs.WriteJSON(w, http.StatusUnprocessableEntity, errBody("insufficient funds"))
		return
	}

	// Move the money: post the ledger entries, then pay out through the provider.
	var ref string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = a.ledger.PostEntries(ctx, transferID, req.From, req.To, req.Amount); err != nil {
			break
		}
		if ref, err = a.provider.Payout(ctx, transferID, req.To, req.Amount); err == nil {
			break
		}
		log.WarnContext(ctx, "payout failed", "attempt", attempt, "err", err)
	}

	status, code := "completed", http.StatusCreated
	if err != nil {
		status, code = "failed", http.StatusBadGateway
	}
	if err := a.insertTransfer(ctx, transferID, key, req, status, ref); err != nil {
		a.fail(ctx, w, http.StatusInternalServerError, "saving transfer failed", err)
		return
	}

	log.InfoContext(ctx, "transfer processed",
		"status", status, "from", req.From, "to", req.To, "amount", req.Amount)
	obs.WriteJSON(w, code, map[string]string{"transfer_id": transferID, "status": status, "provider_ref": ref})
}

func (a *app) fail(ctx context.Context, w http.ResponseWriter, code int, msg string, err error) {
	a.log.ErrorContext(ctx, msg, "err", err)
	obs.WriteJSON(w, code, errBody(msg))
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "trf_" + hex.EncodeToString(b)
}
