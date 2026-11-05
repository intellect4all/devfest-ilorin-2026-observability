package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestLogLinesCarryTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(minLevel{traceIDs{slog.NewJSONHandler(&buf, nil)}, slog.LevelInfo})

	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))

	log.DebugContext(ctx, "dropped below the level")
	log.With("service", "test").WarnContext(ctx, "slow query", "duration_ms", 812)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("want exactly one JSON line, got %q: %v", buf.String(), err)
	}
	if line["trace_id"] != tid.String() || line["span_id"] != sid.String() {
		t.Errorf("trace_id/span_id = %v/%v, want %s/%s", line["trace_id"], line["span_id"], tid, sid)
	}
	if line["level"] != "WARN" || line["service"] != "test" || line["duration_ms"] != float64(812) {
		t.Errorf("unexpected line %v", line)
	}
}
