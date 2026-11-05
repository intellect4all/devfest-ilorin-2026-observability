// Command provider stands in for an external payout switch. Its latency,
// error rate and timeout rate can be changed at runtime through the admin
// listener, so the presenter can make it flaky on stage.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"devfest/internal/httpx"
	"devfest/internal/telemetry"
)

type settings struct {
	Latency     time.Duration // base latency; up to the same again is added as jitter
	ErrorRate   float64       // share of payouts rejected with 502
	TimeoutRate float64       // share of payouts that hang for 10 s
}

type provider struct {
	mu  sync.RWMutex
	cfg settings
}

func main() {
	shutdown, err := telemetry.Setup(context.Background(), "provider")
	if err != nil {
		log.Fatalf("telemetry: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	p := &provider{cfg: settings{
		Latency:     time.Duration(envInt("PROVIDER_LATENCY_MS", 35)) * time.Millisecond,
		ErrorRate:   envFloat("PROVIDER_ERROR_RATE", 0),
		TimeoutRate: envFloat("PROVIDER_TIMEOUT_RATE", 0),
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /payouts", p.payout)
	mux.HandleFunc("GET /healthz", httpx.Healthz)

	api := otelhttp.NewHandler(mux, "provider", otelhttp.WithFilter(telemetry.SkipHealthz))
	httpx.Serve(":"+httpx.Env("PORT", "8082"), api, ":"+httpx.Env("ADMIN_PORT", "7082"), p.adminMux())
}

func (p *provider) current() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

// payout answers POST /payouts {transfer_id, account, amount}.
func (p *provider) payout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		TransferID string `json:"transfer_id"`
		Account    string `json:"account"`
		Amount     int64  `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TransferID == "" {
		httpx.Error(w, http.StatusBadRequest, "transfer_id is required")
		return
	}

	cfg := p.current()
	delay := cfg.Latency + time.Duration(rand.Int64N(int64(cfg.Latency)+1))
	if rand.Float64() < cfg.TimeoutRate {
		delay = 10 * time.Second // longer than any sane client timeout
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		log.Printf("caller gave up on payout %s", req.TransferID)
		return
	}

	if rand.Float64() < cfg.ErrorRate {
		httpx.Error(w, http.StatusBadGateway, "switch unavailable")
		return
	}
	ref := fmt.Sprintf("PRV-%08X", rand.Uint32())
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"provider_ref": ref})
}

// adminMux: GET/POST /admin/config?latency_ms=40&error_rate=0.2&timeout_rate=0.1
func (p *provider) adminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			q := r.URL.Query()
			p.mu.Lock()
			if n, err := strconv.Atoi(q.Get("latency_ms")); err == nil && n >= 0 {
				p.cfg.Latency = time.Duration(n) * time.Millisecond
			}
			if f, err := strconv.ParseFloat(q.Get("error_rate"), 64); err == nil {
				p.cfg.ErrorRate = f
			}
			if f, err := strconv.ParseFloat(q.Get("timeout_rate"), 64); err == nil {
				p.cfg.TimeoutRate = f
			}
			p.mu.Unlock()
		}
		cfg := p.current()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"latency_ms":   cfg.Latency.Milliseconds(),
			"error_rate":   cfg.ErrorRate,
			"timeout_rate": cfg.TimeoutRate,
		})
	})
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	return mux
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return f
	}
	return def
}
