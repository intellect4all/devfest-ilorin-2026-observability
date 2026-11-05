// Package httpx holds the small HTTP helpers every service shares.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Serve runs the public API and the admin listener until SIGINT or SIGTERM,
// then drains both for up to 5 seconds.
//
// The admin listener carries the workshop's bug toggles. It is a separate
// server so that it is never wrapped in instrumentation: flipping a bug must
// not show up in any metric, trace or log.
func Serve(apiAddr string, api http.Handler, adminAddr string, admin http.Handler) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	servers := []*http.Server{
		{Addr: apiAddr, Handler: api, ReadHeaderTimeout: 5 * time.Second},
		{Addr: adminAddr, Handler: admin, ReadHeaderTimeout: 5 * time.Second},
	}
	for _, srv := range servers {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("server on %s failed: %v", srv.Addr, err)
			}
		}()
	}
	log.Printf("listening on %s (admin on %s)", apiAddr, adminAddr)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": msg} with the given status.
func Error(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// Healthz answers liveness checks.
func Healthz(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// Env returns the environment variable, or def when it is unset.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Toggle parses "on"/"off" from the query parameter name; ok is false when
// the parameter is absent or invalid.
func Toggle(r *http.Request, name string) (on, ok bool) {
	switch r.URL.Query().Get(name) {
	case "on":
		return true, true
	case "off":
		return false, true
	}
	return false, false
}
