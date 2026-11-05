// Package obs holds the observability plumbing shared by every service:
// JSON logs that carry the trace ID, RED metrics, tracer setup and a server
// runner with graceful shutdown.
package obs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ---------- Logs ----------

// NewLogger returns a JSON logger. Any record logged with a context that
// carries a span gets trace_id and span_id, which is what joins logs to traces.
func NewLogger(service string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(traceHandler{h}).With("service", service)
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// ---------- Metrics ----------

var requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "http_server_request_duration_seconds",
	Help:    "Duration of handled HTTP requests by service, route and status.",
	Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, .75, 1, 2.5, 5},
}, []string{"service", "route", "method", "status"})

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// RED wraps one route so every request is counted (Rate), classified by
// status (Errors) and timed (Duration). One histogram gives all three.
func RED(service, route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		requestDuration.
			WithLabelValues(service, route, r.Method, strconv.Itoa(rec.status)).
			Observe(time.Since(start).Seconds())
	})
}

// ---------- Traces ----------

// InitTracer wires OpenTelemetry for the ledger and mockprovider services.
// The transfers service has its own copy in transfers/telemetry.go, which is
// the one attendees write live.
func InitTracer(ctx context.Context, service string) (func(context.Context) error, error) {
	exp, err := otlptracehttp.New(ctx) // reads OTEL_EXPORTER_OTLP_ENDPOINT
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// SpanName names server spans "METHOD /path".
func SpanName(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }

// SkipNoise keeps scrapes, health checks and admin toggles out of Jaeger.
func SkipNoise(r *http.Request) bool {
	p := r.URL.Path
	return p != "/metrics" && p != "/healthz" && !strings.HasPrefix(p, "/admin/")
}

// ---------- HTTP helpers ----------

// Serve runs the server until SIGINT or SIGTERM, then drains for up to 5 s.
func Serve(log *slog.Logger, addr string, h http.Handler) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("stopped")
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Healthz answers liveness checks.
func Healthz(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// Env returns the environment variable or a default.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
