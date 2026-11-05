// Command mockprovider stands in for an external payout provider (think a
// NIP-style switch). Latency, error rate and timeout rate are configurable at
// runtime through POST /admin/config.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"devfest-observability/internal/obs"
)

type settings struct {
	Latency     time.Duration
	ErrorRate   float64
	TimeoutRate float64
}

type provider struct {
	mu  sync.RWMutex
	cfg settings
	log *slog.Logger
}

func main() {
	log := obs.NewLogger("mockprovider")
	shutdown, err := obs.InitTracer(context.Background(), "mockprovider")
	if err != nil {
		log.Error("tracer init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	p := &provider{log: log, cfg: settings{
		Latency:     time.Duration(envInt("PROVIDER_LATENCY_MS", 30)) * time.Millisecond,
		ErrorRate:   envFloat("PROVIDER_ERROR_RATE", 0),
		TimeoutRate: envFloat("PROVIDER_TIMEOUT_RATE", 0),
	}}

	mux := http.NewServeMux()
	mux.Handle("POST /payouts", obs.RED("mockprovider", "POST /payouts", http.HandlerFunc(p.payout)))
	mux.HandleFunc("POST /admin/config", p.configure)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", obs.Healthz)

	handler := otelhttp.NewHandler(mux, "mockprovider",
		otelhttp.WithSpanNameFormatter(obs.SpanName),
		otelhttp.WithFilter(obs.SkipNoise),
	)
	obs.Serve(log, ":"+obs.Env("PORT", "8082"), handler)
}

func (p *provider) current() settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

func (p *provider) payout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		TransferID string `json:"transfer_id"`
		Account    string `json:"account"`
		Amount     int64  `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TransferID == "" {
		obs.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "transfer_id is required"})
		return
	}

	cfg := p.current()
	delay := cfg.Latency + time.Duration(rand.Int64N(int64(cfg.Latency)/2+1))
	if rand.Float64() < cfg.TimeoutRate {
		delay = 10 * time.Second // longer than any sane client timeout
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		p.log.WarnContext(ctx, "caller gave up before payout finished", "transfer_id", req.TransferID)
		return
	}

	if rand.Float64() < cfg.ErrorRate {
		p.log.WarnContext(ctx, "payout rejected", "transfer_id", req.TransferID)
		obs.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "provider unavailable"})
		return
	}

	ref := fmt.Sprintf("PRV-%08X", rand.Uint32())
	p.log.InfoContext(ctx, "payout accepted",
		"transfer_id", req.TransferID, "provider_ref", ref, "amount", req.Amount)
	obs.WriteJSON(w, http.StatusCreated, map[string]string{"provider_ref": ref})
}

// configure: POST /admin/config?latency_ms=30&error_rate=0.1&timeout_rate=0.3
func (p *provider) configure(w http.ResponseWriter, r *http.Request) {
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
	cfg := p.cfg
	p.mu.Unlock()

	obs.WriteJSON(w, http.StatusOK, map[string]any{
		"latency_ms":   cfg.Latency.Milliseconds(),
		"error_rate":   cfg.ErrorRate,
		"timeout_rate": cfg.TimeoutRate,
	})
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
