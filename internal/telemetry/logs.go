package telemetry

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// newLogger writes every record twice: as a JSON line on stdout (for
// `docker compose logs`) and through the OTLP bridge to Loki. Both carry the
// trace_id and span_id of the context they were logged with, which is what
// joins a log line to its trace.
func newLogger(service string, lp *sdklog.LoggerProvider) *slog.Logger {
	stdout := traceIDs{slog.NewJSONHandler(os.Stdout, nil)}
	otlp := otelslog.NewHandler(service, otelslog.WithLoggerProvider(lp))
	return slog.New(minLevel{slog.NewMultiHandler(stdout, otlp), level()}).With("service", service)
}

// level reads LOG_LEVEL (debug, info, warn, error); the default is info.
func level() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(os.Getenv("LOG_LEVEL")))); err != nil {
		return slog.LevelInfo
	}
	return l
}

// traceIDs adds trace_id and span_id to records logged with a span context.
type traceIDs struct{ slog.Handler }

func (h traceIDs) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
func (h traceIDs) WithAttrs(as []slog.Attr) slog.Handler { return traceIDs{h.Handler.WithAttrs(as)} }
func (h traceIDs) WithGroup(n string) slog.Handler       { return traceIDs{h.Handler.WithGroup(n)} }

// minLevel drops records below the configured level for every output.
type minLevel struct {
	slog.Handler
	min slog.Level
}

func (h minLevel) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}
func (h minLevel) WithAttrs(as []slog.Attr) slog.Handler {
	return minLevel{h.Handler.WithAttrs(as), h.min}
}
func (h minLevel) WithGroup(n string) slog.Handler { return minLevel{h.Handler.WithGroup(n), h.min} }
