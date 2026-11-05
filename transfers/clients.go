package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var errStatus = errors.New("unexpected status")

func newHTTPClient(timeout time.Duration) *http.Client {
	// STEP2-ALT: return &http.Client{Timeout: timeout}
	// >>> STEP2
	return &http.Client{
		Timeout: timeout,
		// Injects the traceparent header into every outgoing request
		// and records a client span for it.
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	// <<< STEP2
}

// doJSON sends in as JSON and decodes the response into out.
func doJSON(ctx context.Context, c *http.Client, method, target string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	// STEP2-ALT: req, err := http.NewRequest(method, target, body)
	// >>> STEP2
	// Build the request WITH the context: in Go, the context is the trace.
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	// <<< STEP2
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%s %s: %w %d", method, target, errStatus, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ---------- ledger ----------

type ledgerClient struct {
	base string
	http *http.Client
}

func newLedgerClient(base string) *ledgerClient {
	return &ledgerClient{base: base, http: newHTTPClient(5 * time.Second)}
}

func (l *ledgerClient) Balance(ctx context.Context, account string) (int64, error) {
	var out struct {
		Balance int64 `json:"balance"`
	}
	err := doJSON(ctx, l.http, http.MethodGet, l.base+"/balance?account="+url.QueryEscape(account), nil, &out)
	return out.Balance, err
}

func (l *ledgerClient) PostEntries(ctx context.Context, transferID, from, to string, amount int64) error {
	in := map[string]any{"transfer_id": transferID, "from": from, "to": to, "amount": amount}
	return doJSON(ctx, l.http, http.MethodPost, l.base+"/entries", in, nil)
}

// ---------- provider ----------

type providerClient struct {
	base string
	http *http.Client
}

func newProviderClient(base string) *providerClient {
	return &providerClient{base: base, http: newHTTPClient(1 * time.Second)}
}

func (p *providerClient) Payout(ctx context.Context, transferID, account string, amount int64) (string, error) {
	var out struct {
		Ref string `json:"provider_ref"`
	}
	in := map[string]any{"transfer_id": transferID, "account": account, "amount": amount}
	err := doJSON(ctx, p.http, http.MethodPost, p.base+"/payouts", in, &out)
	return out.Ref, err
}
