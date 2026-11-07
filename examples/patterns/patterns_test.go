package patterns

// These tests check that the ✅ GOOD examples do what their comments claim,
// so nothing on the projector is wrong.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	var h slog.Handler = slog.NewJSONHandler(buf, &slog.HandlerOptions{ReplaceAttr: redact})
	return slog.New(traceHandler{h})
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestTransferLogValueMasksAndDropsSecrets(t *testing.T) {
	var buf bytes.Buffer
	tr := Transfer{ID: "t1", From: "0123456789", To: "12", Kobo: 5000, PIN: "1234", Narration: "rent"}
	jsonLogger(&buf).Info("transfer", "transfer", tr)

	s := buf.String()
	for _, leak := range []string{"0123456789", "1234\"", "rent"} {
		if strings.Contains(s, leak) {
			t.Errorf("log leaks %q: %s", leak, s)
		}
	}
	got := lines(t, &buf)[0]["transfer"].(map[string]any)
	if got["from"] != "****6789" || got["to"] != "****" || got["amount_kobo"] != float64(5000) {
		t.Errorf("unexpected transfer group: %v", got)
	}
}

func TestRedactBlocklist(t *testing.T) {
	var buf bytes.Buffer
	jsonLogger(&buf).Info("login", "Password", "hunter2", "card_number", "4111", "user", "ade")
	l := lines(t, &buf)[0]
	if l["Password"] != "[REDACTED]" || l["card_number"] != "[REDACTED]" || l["user"] != "ade" {
		t.Errorf("redact: %v", l)
	}
}

func TestTraceHandlerAddsTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()

	var buf bytes.Buffer
	lg := jsonLogger(&buf)
	lg.InfoContext(ctx, "with span")
	lg.InfoContext(context.Background(), "without span")

	ls := lines(t, &buf)
	if ls[0]["trace_id"] != span.SpanContext().TraceID().String() || ls[0]["span_id"] == nil {
		t.Errorf("trace_id missing or wrong: %v", ls[0])
	}
	if _, ok := ls[1]["trace_id"]; ok {
		t.Errorf("trace_id on a line without a span: %v", ls[1])
	}
}

func TestSamplerKeepsWarnAndError(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(newSampler(slog.NewJSONHandler(&buf, nil), 2, 10))
	for range 50 {
		lg.Info("hot")
		lg.Error("boom")
	}
	var info, errs, sampled int
	for _, l := range lines(t, &buf) {
		switch l["msg"] {
		case "hot":
			info++
			if l["sampled_out"] != nil {
				sampled++
			}
		case "boom":
			errs++
		}
	}
	if errs != 50 {
		t.Errorf("ERROR lines kept = %d, want 50", errs)
	}
	// 2 first + every 10th of the remaining 48 = 2 + 4. The loop can cross a
	// second boundary, which resets the counts, so allow a little slack.
	if info < 6 || info > 12 || sampled == 0 {
		t.Errorf("INFO kept = %d (sampled markers %d), want about 6", info, sampled)
	}
}

func TestProductionSamplerFollowsParent(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(ProductionSampler(0)))
	tr := tp.Tracer("t")

	_, root := tr.Start(context.Background(), "root")
	if root.SpanContext().IsSampled() {
		t.Error("ratio 0 should not sample a new root")
	}

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1},
		TraceFlags: trace.FlagsSampled, Remote: true,
	})
	_, child := tr.Start(trace.ContextWithRemoteSpanContext(context.Background(), parent), "child")
	if !child.SpanContext().IsSampled() {
		t.Error("ParentBased should keep a span whose upstream parent was sampled")
	}
}

func TestREDMiddlewareUsesRoutePattern(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /transfers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := REDMiddleware(mux)
	for _, id := range []string{"a1", "b2", "c3"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/transfers/"+id, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/nope", nil))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	routes := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http.server.request.duration" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				r, _ := dp.Attributes.Value("http.route")
				routes[r.AsString()] += dp.Count
				if r.AsString() == "GET /transfers/{id}" {
					if s, _ := dp.Attributes.Value("http.response.status_code"); s.AsInt64() != 418 {
						t.Errorf("status = %d, want 418", s.AsInt64())
					}
				}
			}
		}
	}
	if routes["GET /transfers/{id}"] != 3 || routes["unmatched"] != 1 || len(routes) != 2 {
		t.Errorf("want 3 requests on the route pattern and 1 unmatched, got %v", routes)
	}
}
