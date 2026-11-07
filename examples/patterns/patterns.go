// Package patterns shows metrics, logs and traces in Go the way they look in
// production code, next to the mistakes that keep showing up in real
// services. It is written to be read on a projector, top to bottom.
//
//	✅ GOOD  do this
//	❌ BAD   real code does this; the comment says what it costs you
//
// The domain is the workshop's: a transfers API that calls a ledger and a
// payout provider, backed by Postgres. Everything uses OpenTelemetry for
// metrics and traces and log/slog for logs, exported over OTLP.
//
// Contents
//
//  0. Setup: providers, resource, exporters, sampling, shutdown
//  1. Metrics
//     1.1 Naming and units
//     1.2 Counter
//     1.3 UpDownCounter
//     1.4 Gauge (observable and synchronous)
//     1.5 Histogram and buckets
//     1.6 Attributes and cardinality
//     1.7 RED middleware for HTTP
//     1.8 Exemplars: the link from a metric to a trace
//     1.9 Views: change buckets and drop attributes without code changes
//  2. Logs
//     2.1 Logger setup: JSON, levels, trace_id on every line
//     2.2 Structured fields, not sentences
//     2.3 Levels: what goes where
//     2.4 Errors: log once, at the edge
//     2.5 Secrets and personal data
//     2.6 Hot paths and log sampling
//  3. Traces
//     3.1 Starting and ending spans
//     3.2 Span names
//     3.3 Attributes, events, errors and status
//     3.4 Propagation between services
//     3.5 Goroutines, queues and span links
//     3.6 Sampling
//     3.7 Too many spans
//  4. All three together: one handler done right, one done wrong
//
// Which signal answers what:
//
//	metrics  IS something wrong, and how much?  cheap, aggregated, alertable
//	traces   WHERE is the time going?           one request across services
//	logs     WHY did this one request fail?     the details, joined by trace_id
//
// Rule of thumb for IDs (user, account, transfer, trace): they belong on
// spans and log lines, never on metric attributes. Section 1.6 says why.
package patterns

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// ═════════════════════════════════════════════════════════════════════════
// 0. SETUP
// ═════════════════════════════════════════════════════════════════════════

// ✅ GOOD: one Setup per process, called first thing in main.
//   - A resource says who is sending: service name, version, environment.
//     Every metric, span and log line carries it.
//   - Exporters batch in the background, so a slow Collector never slows a
//     request down.
//   - Endpoints come from the standard OTEL_* environment variables, so the
//     same binary runs on a laptop and in Kubernetes.
//   - The returned shutdown flushes what is still buffered.
func Setup(ctx context.Context, service, version string) (func(context.Context) error, error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(service),
		semconv.ServiceVersion(version), // compare before and after a deploy
		semconv.DeploymentEnvironmentName(os.Getenv("DEPLOY_ENV")),
	))
	if err != nil {
		return nil, err
	}

	// Traces. Reads OTEL_EXPORTER_OTLP_ENDPOINT.
	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithSampler(ProductionSampler(0.10)),
	)
	otel.SetTracerProvider(tp)

	// W3C traceparent and baggage headers between services. Without this
	// line every service starts its own trace and the waterfall is broken.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithView(Views()...),
	)
	otel.SetMeterProvider(mp)

	// Logs: see NewLogger in section 2.1.

	return func(ctx context.Context) error {
		// Traces first: their batch is the one most likely to hold the
		// request that was in flight when the process was told to stop.
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

// ✅ GOOD: main gives shutdown a deadline, so a dead Collector can't hang
// the deploy, and runs it on every exit path.
func exampleMain() {
	ctx := context.Background()
	shutdown, err := Setup(ctx, "transfers", "1.4.2")
	if err != nil {
		log.Fatalf("telemetry: %v", err) // fine in main, nowhere else
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "telemetry shutdown:", err)
		}
	}()
	// ... run the server until SIGTERM ...
}

// ❌ BAD: os.Exit and log.Fatal skip deferred calls. The last few seconds
// of spans and metrics, usually the ones explaining the crash, are never
// sent.
func badMain() {
	shutdown, _ := Setup(context.Background(), "transfers", "dev") // ❌ error ignored
	defer shutdown(context.Background())                           // ❌ no deadline
	if err := run(); err != nil {
		os.Exit(1) // ❌ the deferred shutdown never runs
	}
}

func run() error { return nil }

// ❌ BAD: a synchronous exporter. Every span.End() now waits on a network
// call to the Collector, inside your request.
func badTracerProvider(exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
}

// ═════════════════════════════════════════════════════════════════════════
// 1. METRICS
// ═════════════════════════════════════════════════════════════════════════
//
// The instrument types and when to use each:
//
//	Counter        only goes up: requests, errors, bytes sent, money moved
//	UpDownCounter  goes up and down, you change it: in-flight, queue depth
//	Gauge          a value you read from somewhere: pool size, temperature
//	Histogram      a distribution: latency, payload size, amounts
//
// Create instruments ONCE, at start-up, and reuse them. Creating one is a
// map lookup behind a lock; recording into one is a few atomic adds.

// meter is named after the package that records, so you can tell which
// library a metric came from.
var meter = otel.Meter("devfest/transfers")

// ─── 1.1 Naming and units ────────────────────────────────────────────────
//
// ✅ GOOD
//   - dot.separated, lower case, namespaced: payments.transfers.created
//   - the unit goes in WithUnit, never in the name: "s", "By", "{transfer}"
//   - seconds for durations, bytes for sizes (UCUM units)
//   - follow OpenTelemetry semantic conventions where one exists:
//     http.server.request.duration, db.client.operation.duration
//
// Prometheus turns http.server.request.duration with unit "s" into
// http_server_request_duration_seconds_bucket / _sum / _count by itself,
// and adds _total to counters.
//
// ❌ BAD
//
//	"TransferLatencyMs"               camel case, unit baked into the name
//	"transfer_latency_seconds" + "ms" name and unit disagree: every
//	                                  dashboard is out by 1000x
//	"transfers_total"                 Prometheus adds _total; this becomes
//	                                  transfers_total_total
//	"api.v2.prod.transfers"           version and environment are
//	                                  attributes, not part of the name

// ─── 1.2 Counter ─────────────────────────────────────────────────────────

// ✅ GOOD: a monotonic counter with a few bounded attributes. You never
// read the raw number; you take rate() of it:
//
//	sum by (status) (rate(payments_transfers_created_total[5m]))
var transfersCreated, _ = meter.Int64Counter("payments.transfers.created",
	metric.WithUnit("{transfer}"),
	metric.WithDescription("Transfers accepted by the API, by final status."))

// ✅ GOOD: money as an integer count of the smallest unit (kobo), with
// the currency as an attribute. Floats drift; you can't reconcile with
// them.
//
//	sum by (currency) (increase(payments_transfers_amount_total[1h])) / 100
var transferAmount, _ = meter.Int64Counter("payments.transfers.amount",
	metric.WithUnit("{kobo}"),
	metric.WithDescription("Value of successful transfers in minor units."))

// Precomputed attribute sets: zero allocations on the hot path.
var (
	statusSucceeded = metric.WithAttributeSet(
		attribute.NewSet(attribute.String("status", "succeeded")))
	statusFailed = metric.WithAttributeSet(
		attribute.NewSet(attribute.String("status", "failed")))
)

func recordTransfer(ctx context.Context, kobo int64, currency string, err error) {
	if err != nil {
		transfersCreated.Add(ctx, 1, statusFailed)
		return
	}
	transfersCreated.Add(ctx, 1, statusSucceeded)
	transferAmount.Add(ctx, kobo, metric.WithAttributes(attribute.String("currency", currency)))
}

// ❌ BAD: four mistakes common enough to have names.
func badCounters(ctx context.Context, userID string, kobo int64, err error) {
	// ❌ Instrument created per call: a lock and a map lookup on every
	//    request, and errors from it silently dropped.
	c, _ := otel.Meter("x").Int64Counter("transfers")

	// ❌ Unbounded attribute: one time series per user. 100k users is 100k
	//    series per pod; Prometheus runs out of memory, your bill explodes,
	//    and no dashboard can draw it anyway. user.id belongs on the span.
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("user.id", userID)))

	// ❌ The error message as an attribute: every distinct message, with
	//    its embedded IDs and timestamps, is a new series.
	if err != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("error", err.Error())))
	}

	// ❌ A counter that goes down. Counters must be monotonic; the SDK drops
	//    negative adds, and rate() reads a reset as a crash. Use an
	//    UpDownCounter (1.3).
	c.Add(ctx, -1)

	// ❌ Float money: 0.1 + 0.2 != 0.3, and finance will find out.
	f, _ := meter.Float64Counter("amount_naira")
	f.Add(ctx, float64(kobo)/100)
}

// ─── 1.3 UpDownCounter ───────────────────────────────────────────────────

// ✅ GOOD: something you increment and decrement yourself.
var inFlight, _ = meter.Int64UpDownCounter("http.server.active_requests",
	metric.WithUnit("{request}"),
	metric.WithDescription("Requests being handled right now."))

func trackInFlight(ctx context.Context, route string, handle func()) {
	attrs := metric.WithAttributes(attribute.String("http.route", route))
	inFlight.Add(ctx, 1, attrs)
	defer inFlight.Add(ctx, -1, attrs) // ✅ defer: runs on panic and early return
	handle()
}

// ❌ BAD: the decrement is skipped on the error path. The gauge creeps up
// by one per failure, forever, and someone gets paged for a leak that
// isn't there.
func badInFlight(ctx context.Context, handle func() error) error {
	inFlight.Add(ctx, 1)
	if err := handle(); err != nil {
		return err // ❌ never decremented
	}
	inFlight.Add(ctx, -1)
	return nil
}

// ─── 1.4 Gauge ───────────────────────────────────────────────────────────

// ✅ GOOD: an observable gauge. You don't record it; the SDK calls you
// when it collects. Right for anything you can read on demand: pool stats,
// cache size, goroutines, how old the oldest queued job is.
func RegisterPoolMetrics(db *sql.DB) error {
	inUse, err := meter.Int64ObservableGauge("db.client.connection.count",
		metric.WithUnit("{connection}"),
		metric.WithDescription("Open connections, by state."))
	if err != nil {
		return err
	}
	waits, err := meter.Int64ObservableCounter("db.client.connection.wait_count",
		metric.WithUnit("{wait}"),
		metric.WithDescription("Times a query waited for a free connection."))
	if err != nil {
		return err
	}
	used := metric.WithAttributes(attribute.String("state", "used"))
	idle := metric.WithAttributes(attribute.String("state", "idle"))

	// ✅ One callback for several instruments: db.Stats() is read once per
	// collection, so the numbers agree with each other.
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := db.Stats()
		o.ObserveInt64(inUse, int64(s.InUse), used)
		o.ObserveInt64(inUse, int64(s.Idle), idle)
		o.ObserveInt64(waits, s.WaitCount) // cumulative, so a counter
		return nil
	}, inUse, waits)
	return err
	// Saturation alert:
	//   db_client_connection_count{state="used"} / on() max_open_conns > 0.9
}

// ✅ GOOD: a synchronous gauge, for a value you are told rather than one
// you can read: the latest FX rate from a feed, a config value.
var fxRate, _ = meter.Float64Gauge("payments.fx.rate",
	metric.WithUnit("{NGN}/{USD}"),
	metric.WithDescription("Last NGN per USD rate received from the feed."))

func onRate(ctx context.Context, rate float64) {
	fxRate.Record(ctx, rate, metric.WithAttributes(attribute.String("source", "feed-a")))
}

// ❌ BAD: a gauge for latency. Only the last value before each collection
// survives; the one slow request in a thousand is gone. Latency is always
// a histogram (1.5).
func badLatencyGauge(ctx context.Context, d time.Duration) {
	g, _ := meter.Float64Gauge("last_request_latency")
	g.Record(ctx, d.Seconds())
}

// ❌ BAD: polling runtime stats in your own goroutine. It runs whether or
// not anyone collects, races with shutdown, and is one more goroutine to
// leak. Use an observable gauge, or go.opentelemetry.io/contrib/
// instrumentation/runtime, which already does this.
func badGoroutineGauge(ctx context.Context) {
	g, _ := meter.Int64Gauge("goroutines")
	go func() {
		for {
			g.Record(ctx, int64(runtime.NumGoroutine()))
			time.Sleep(time.Second)
		}
	}()
}

// ─── 1.5 Histogram and buckets ───────────────────────────────────────────
//
// A histogram counts observations into buckets. From the buckets you get
// percentiles at query time, across every pod:
//
//	histogram_quantile(0.99,
//	  sum by (le, http_route) (rate(http_server_request_duration_seconds_bucket[5m])))
//
// Percentiles are only as good as the buckets around them. The answer is
// interpolated inside a bucket, so put boundaries where your decisions are:
// the SLO threshold, and a few either side of it.

// ✅ GOOD: seconds, with boundaries around a 250 ms SLO. Fine where the
// SLO is (0.1, 0.25, 0.5), coarse far from it.
var requestDuration, _ = meter.Float64Histogram("http.server.request.duration",
	metric.WithUnit("s"),
	metric.WithDescription("Time to serve an HTTP request."),
	metric.WithExplicitBucketBoundaries(
		0.005, 0.01, 0.025, 0.05, 0.1, 0.15, 0.2, 0.25, 0.3, 0.5, 0.75, 1, 2.5, 5, 10))

// ✅ GOOD: the SLO as a query, straight off the bucket at the threshold:
//
//	sum(rate(http_server_request_duration_seconds_bucket{le="0.25"}[5m]))
//	  / sum(rate(http_server_request_duration_seconds_count[5m]))
//
// "share of requests faster than 250 ms". It only works because 0.25 is a
// boundary.

// ✅ GOOD: sizes and amounts are distributions too. Exponential buckets
// for values that span orders of magnitude.
var transferSize, _ = meter.Int64Histogram("payments.transfers.size",
	metric.WithUnit("{kobo}"),
	metric.WithDescription("Distribution of transfer amounts."),
	metric.WithExplicitBucketBoundaries(
		1_000_00, 10_000_00, 50_000_00, 100_000_00, 500_000_00, 1_000_000_00, 5_000_000_00))

// ❌ BAD: four ways histograms go wrong.
func badHistograms(ctx context.Context, start time.Time) {
	// ❌ The SDK's default boundaries (0, 5, 10, 25 ... 10000) were made
	//    for milliseconds. With seconds, every request lands in the first
	//    two buckets and p99 reads "somewhere under 5 seconds".
	h, _ := meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	h.Record(ctx, time.Since(start).Seconds())

	// ❌ Unit says seconds, value is milliseconds. Off by 1000x everywhere.
	h.Record(ctx, float64(time.Since(start).Milliseconds()))

	// ❌ Averages computed in the app. You can't add averages across pods,
	//    and the average hides the slow tail, which is the part users feel.
	avg, _ := meter.Float64Gauge("avg_latency")
	avg.Record(ctx, 0.042)

	// ❌ A counter of total seconds plus a counter of requests "to compute
	//    the mean later". Same problem: no percentiles, no tail.
}

// ─── 1.6 Attributes and cardinality ──────────────────────────────────────
//
// Every distinct combination of attribute values is its own time series,
// stored for weeks. Series count = product of the number of values of
// each attribute:
//
//	route (12) x method (4) x status (8)          =     384 series  ✅
//	... x user.id (100 000)                       = 38.4 M series   ❌
//
// ✅ Bounded attributes: route TEMPLATE, method, status code, error class,
//    region, currency, provider name.
// ❌ Unbounded: user/account/transfer IDs, raw URL paths, emails, IPs,
//    error messages, timestamps, amounts.

// ✅ GOOD: turn errors into a small, fixed set of classes before they get
// anywhere near an attribute.
var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrProviderTimeout   = errors.New("provider timeout")
)

func errorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInsufficientFunds):
		return "insufficient_funds"
	case errors.Is(err, ErrProviderTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "internal" // ✅ a catch-all keeps the set closed
	}
}

// ✅ GOOD: an allow-list for anything that comes from the outside world.
func providerLabel(name string) string {
	switch name {
	case "paystack", "flutterwave", "moniepoint":
		return name
	default:
		return "other" // a typo or a new partner can't create series
	}
}

// ─── 1.7 RED middleware for HTTP ─────────────────────────────────────────
//
// Rate, Errors, Duration for every route, from one histogram: _count is the
// rate, the status attribute gives errors, the buckets give duration.
//
// In real code use otelhttp.NewHandler (section 3.4), which does this and
// the tracing too. This version shows what it does inside.

// ✅ GOOD
func REDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// ✅ r.Pattern is the ServeMux route ("POST /transfers/{id}"), set
		//    during routing, so read it after next has run. Never r.URL.Path.
		route := r.Pattern
		if route == "" {
			route = "unmatched" // 404s from scanners stay one series
		}
		requestDuration.Record(r.Context(), time.Since(start).Seconds(),
			metric.WithAttributes(
				semconv.HTTPRoute(route),
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.HTTPResponseStatusCode(rec.status),
			))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// ❌ BAD: the raw path as an attribute. /transfers/8c1f..., /transfers/9a2e...
// each make a new series; one crawler can add millions overnight.
func badREDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r) // ❌ and the status code is never captured
		requestDuration.Record(context.Background(), // ❌ ctx dropped: no exemplar (1.8)
			float64(time.Since(start).Milliseconds()), // ❌ ms into a seconds histogram
			metric.WithAttributes(
				attribute.String("path", r.URL.Path),          // ❌ unbounded
				attribute.String("query", r.URL.RawQuery),     // ❌ unbounded, and leaks data
				attribute.String("user_agent", r.UserAgent()), // ❌ unbounded
				attribute.String("client_ip", r.RemoteAddr),   // ❌ unbounded, and personal data
			))
	})
}

// ─── 1.8 Exemplars ───────────────────────────────────────────────────────
//
// An exemplar is a sample trace_id stored next to a histogram bucket. In
// Grafana it is the dot on the latency graph you click to open the exact
// slow trace. The SDK attaches one automatically when you record with a
// context that holds a sampled span.
//
// ✅ GOOD: requestDuration.Record(r.Context(), ...)        exemplar attached
// ❌ BAD:  requestDuration.Record(context.Background(), ...) no exemplar,
//          no click-through, and nobody notices until the incident

// ─── 1.9 Views ───────────────────────────────────────────────────────────

// Views reshape a metric at the SDK without touching the code that records
// it: new buckets, fewer attributes, a different aggregation, or dropping a
// noisy metric from a library you don't own.
func Views() []sdkmetric.View {
	return []sdkmetric.View{
		// ✅ Retune buckets for one instrument after you learn the real
		// latency profile.
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "db.client.operation.duration"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 1},
			}},
		),
		// ✅ Exponential histograms: buckets adapt to the data, high
		// resolution for a fixed size. Needs Prometheus native histograms.
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "payments.transfers.size"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{
				MaxSize: 160, MaxScale: 20,
			}},
		),
		// ✅ A library added an attribute you can't afford: drop it here.
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "http.client.*"},
			sdkmetric.Stream{AttributeFilter: attribute.NewDenyKeysFilter(
				"url.full", "server.address")},
		),
		// ✅ Turn off a metric you never look at.
		sdkmetric.NewView(
			sdkmetric.Instrument{Name: "rpc.server.requests_per_rpc"},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationDrop{}},
		),
	}
}

// ═════════════════════════════════════════════════════════════════════════
// 2. LOGS
// ═════════════════════════════════════════════════════════════════════════
//
// Production logs are events for machines first: one JSON object per line,
// fixed keys, searchable in Loki or Elasticsearch, and joined to traces by
// trace_id.

// ─── 2.1 Logger setup ────────────────────────────────────────────────────

// LogLevel can be changed at runtime, for example from an admin endpoint,
// to turn on debug for ten minutes without a redeploy.
var LogLevel = new(slog.LevelVar) // starts at Info

// ✅ GOOD: JSON to stdout (the platform ships it), a level you can change,
// service fields on every line, trace_id and span_id from the context,
// secrets scrubbed, and sampling for the noisy levels (2.6).
func NewLogger(service, version string) *slog.Logger {
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       LogLevel,
		AddSource:   false,  // ✅ off: costs a runtime.Caller per line
		ReplaceAttr: redact, // 2.5
	})
	h = traceHandler{h}
	h = newSampler(h, 20, 100) // 2.6
	return slog.New(h).With(
		slog.String("service", service),
		slog.String("version", version),
	)
	// To ship over OTLP as well, combine this with the bridge:
	//   slog.NewMultiHandler(h, otelslog.NewHandler(service))
	// (go.opentelemetry.io/contrib/bridges/otelslog)
}

// traceHandler adds trace_id and span_id to every record logged with a
// span in its context. This is what makes "Related logs" work in Grafana.
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
func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}
func (h traceHandler) WithGroup(n string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(n)}
}

// A line from NewLogger looks like this:
//
//	{"time":"2026-11-07T10:42:13.120Z","level":"WARN","msg":"waited for account lock",
//	 "service":"ledger","version":"1.4.2","account":"****4821","wait_ms":432,
//	 "held_by_step":"payout","trace_id":"35223f59bbc6f84f9c5187849aa119ff",
//	 "span_id":"a3c1e0f2b4d67788"}

// ❌ BAD: the standard log package in a service. Plain text, no levels, no
// fields, no trace_id. Fine for a CLI tool; useless for searching across 40
// pods.
func badLoggerSetup() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("starting transfers on :8080")
}

// ─── 2.2 Structured fields, not sentences ────────────────────────────────

// ✅ GOOD
//   - a constant message: "transfer completed", so you can count it
//   - the variable parts as typed fields: amount is a number, not text
//   - the same key everywhere for the same thing (pick a list and keep it:
//     transfer_id, account, amount_kobo, duration_ms, error)
//   - InfoContext, so the trace_id comes along
//   - request-scoped fields attached once with With
func logGood(ctx context.Context, logger *slog.Logger,
	transferID string, kobo int64, took time.Duration) {
	logger = logger.With(slog.String("transfer_id", transferID)) // every line below has it

	logger.InfoContext(ctx, "transfer completed",
		slog.Int64("amount_kobo", kobo),
		slog.Int64("duration_ms", took.Milliseconds()),
		slog.Group("provider", // → "provider":{"name":"paystack","attempts":2}
			slog.String("name", "paystack"),
			slog.Int("attempts", 2),
		),
	)
	// Loki: {service_name="transfers"} | json | duration_ms > 500
}

// ❌ BAD
func logBad(ctx context.Context, transferID string, kobo int64, took time.Duration) {
	// ❌ Variables baked into the message: every line is unique, you can't
	//    group or count them, and you search with regexes.
	slog.Info(fmt.Sprintf("Transfer %s completed for %d kobo in %v", transferID, kobo, took))

	// ❌ No context: this line has no trace_id and can't be joined to the
	//    trace. slog.Info → slog.InfoContext(ctx, ...).
	slog.Info("transfer completed", "transfer_id", transferID)

	// ❌ Same thing, different keys in different files: transferId,
	//    transfer_id, txn, id. Every query has to know all four.
	slog.InfoContext(ctx, "transfer completed", "transferId", transferID)
	slog.InfoContext(ctx, "transfer completed", "txn", transferID)

	// ❌ Numbers as strings: you can't filter on duration > 500.
	slog.InfoContext(ctx, "transfer completed", "duration", took.String())

	// ❌ Odd number of key/values: slog logs "!BADKEY". go vet catches this;
	//    run it in CI.
	// slog.InfoContext(ctx, "transfer completed", "transfer_id")
}

// ─── 2.3 Levels ──────────────────────────────────────────────────────────
//
//	DEBUG  details for whoever is debugging right now. Off in production
//	       unless LogLevel is turned up for a while.
//	INFO   things that happened and someone may ask about: started, a
//	       transfer completed, config loaded. Not one per function call.
//	WARN   unusual but handled: a retry worked, a lock wait was long, a
//	       fallback was used. Worth a look if it keeps happening.
//	ERROR  a request or job failed and someone may need to act. If nobody
//	       would act on it, it isn't an ERROR.
//
// ❌ BAD level choices seen in real services:
//   - ERROR for a client's validation mistake (a 400). Your error dashboards
//     fill with other people's typos. That's INFO, or a metric.
//   - INFO for "entering function X" / "leaving function X". That's what
//     traces are for.
//   - WARN that nobody ever reads. Either fix the cause or drop the line.

func logLevels(ctx context.Context, logger *slog.Logger, err error, waited time.Duration) {
	logger.DebugContext(ctx, "lock acquired", slog.String("account", "****4821"))

	if waited > 100*time.Millisecond { // ✅ WARN only past a threshold
		logger.WarnContext(ctx, "waited for account lock",
			slog.Int64("wait_ms", waited.Milliseconds()),
			slog.String("held_by_step", "payout"))
	}

	var ve *ValidationError
	if errors.As(err, &ve) { // ✅ the client's mistake: INFO, not ERROR
		logger.InfoContext(ctx, "transfer rejected", slog.String("reason", ve.Field))
	}
}

type ValidationError struct{ Field string }

func (e *ValidationError) Error() string { return "invalid " + e.Field }

// ─── 2.4 Errors: log once, at the edge ───────────────────────────────────

// ✅ GOOD: inner functions wrap and return. Only the edge (the HTTP
// handler, the job runner) logs, once, with the whole chain.
func debit(ctx context.Context, account string, kobo int64) error {
	if err := postLedger(ctx, account, kobo); err != nil {
		return fmt.Errorf("debit %s: %w", account, err) // ✅ wrap, add context, return
	}
	return nil
}

func handleTransferEdge(ctx context.Context, logger *slog.Logger) {
	if err := debit(ctx, "acc_1", 5000); err != nil {
		logger.ErrorContext(ctx, "transfer failed",
			slog.String("error", err.Error()), // "debit acc_1: post ledger: timeout"
			slog.String("error_class", errorClass(err))) // ✅ groupable
	}
}

// ❌ BAD: log-and-return at every level. One failure, three ERROR lines,
// three alerts, and the incident channel thinks three things broke.
func badDebit(ctx context.Context, account string, kobo int64) error {
	if err := postLedger(ctx, account, kobo); err != nil {
		slog.ErrorContext(ctx, "post ledger failed", "error", err) // ❌ 1st
		return err                                                 // ❌ and no wrapping
	}
	return nil
}

func badHandler(ctx context.Context) {
	if err := badDebit(ctx, "acc_1", 5000); err != nil {
		slog.ErrorContext(ctx, "debit failed", "error", err)    // ❌ 2nd
		slog.ErrorContext(ctx, "transfer failed", "error", err) // ❌ 3rd
	}
}

// ❌ BAD: swallowing the error and logging nothing useful.
func badSwallow(ctx context.Context) {
	if err := postLedger(ctx, "acc_1", 5000); err != nil {
		slog.ErrorContext(ctx, "something went wrong") // ❌ what? where? which account?
	}
}

func postLedger(ctx context.Context, account string, kobo int64) error { return nil }

// ─── 2.5 Secrets and personal data ───────────────────────────────────────
//
// Logs are copied to more places, kept longer and read by more people than
// your database. Treat every field as public.

// ✅ GOOD: the type knows how to log itself. Whoever logs it later, in
// whatever file, gets the masked form.
type AccountNumber string

func (a AccountNumber) LogValue() slog.Value {
	s := string(a)
	if len(s) <= 4 {
		return slog.StringValue("****")
	}
	return slog.StringValue("****" + s[len(s)-4:])
}

type Transfer struct {
	ID        string
	From      AccountNumber
	To        AccountNumber
	Kobo      int64
	PIN       string
	Narration string
}

// ✅ GOOD: choose exactly the fields that go into the log.
func (t Transfer) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", t.ID),
		slog.Any("from", t.From), // masked by AccountNumber.LogValue
		slog.Any("to", t.To),
		slog.Int64("amount_kobo", t.Kobo),
		// PIN and narration are left out on purpose
	)
}

// ✅ GOOD: a last line of defence for keys that should never appear,
// whatever the caller passes.
func redact(_ []string, a slog.Attr) slog.Attr {
	switch strings.ToLower(a.Key) {
	case "password", "pin", "otp", "authorization", "cookie", "token", "api_key", "bvn", "card_number":
		return slog.String(a.Key, "[REDACTED]")
	}
	return a
}

// ❌ BAD
func badPII(ctx context.Context, r *http.Request, t Transfer, cardNumber string) {
	// ❌ Authorization header and cookies, in plain text
	slog.InfoContext(ctx, "request", "headers", r.Header)

	// ❌ the whole struct: PIN and full account numbers (the conversion
	//    drops the LogValue method, which is exactly what %+v does too)
	type rawTransfer Transfer
	slog.InfoContext(ctx, "transfer", "body", fmt.Sprintf("%+v", rawTransfer(t)))

	// ❌ a card number in a log is a PCI-DSS incident
	slog.InfoContext(ctx, "charging card", "pan", cardNumber)

	// ❌ personal data you now have to find and delete on request
	slog.InfoContext(ctx, "login", "email", "ada@example.com")

	// ❌ URLs often carry tokens: ?reset_token=...
	slog.InfoContext(ctx, "callback", "url", r.URL.String())
}

// ─── 2.6 Hot paths and log sampling ──────────────────────────────────────

// ✅ GOOD: in a loop, one summary line instead of one line per item.
func settleBatch(ctx context.Context, logger *slog.Logger, ids []string) {
	start, failed := time.Now(), 0
	for _, id := range ids {
		if err := settle(ctx, id); err != nil {
			failed++
			if failed <= 5 { // ✅ a few examples, not 50 000
				logger.WarnContext(ctx, "settle failed", slog.String("transfer_id", id),
					slog.String("error", err.Error()))
			}
		}
	}
	logger.InfoContext(ctx, "batch settled",
		slog.Int("count", len(ids)), slog.Int("failed", failed),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()))
}

// ✅ GOOD: if building a field is expensive, check the level first.
func logExpensive(ctx context.Context, logger *slog.Logger, state map[string]int) {
	if logger.Enabled(ctx, slog.LevelDebug) {
		logger.DebugContext(ctx, "cache state", slog.String("dump", dump(state)))
	}
}

// ❌ BAD: one line per row at 2 000 requests a second is 170 million lines
// a day. You pay to ship, index and store every one, and the real error
// scrolls away.
func badSettleBatch(ctx context.Context, ids []string) {
	for _, id := range ids {
		slog.InfoContext(ctx, "settling transfer", "transfer_id", id) // ❌ per item
		_ = settle(ctx, id)
		slog.InfoContext(ctx, "settled transfer", "transfer_id", id) // ❌ twice per item
	}
	// ❌ built even when debug is off: dump runs on every call
	slog.DebugContext(ctx, "state", "dump", dump(map[string]int{}))
}

func settle(ctx context.Context, id string) error { return nil }
func dump(m map[string]int) string                { return fmt.Sprint(m) }

// sampler keeps the first `first` records with the same message every
// second, then one in `every`. WARN and ERROR are always kept: those are
// the lines you need during an incident, when volume is highest.
//
// The same idea as zap's sampler and the Collector's log sampling. A
// line that was dropped is counted, so "sampled_out" tells you how much.
type sampler struct {
	slog.Handler
	first, every int
	st           *samplerState
}

type samplerState struct {
	mu     sync.Mutex
	second int64
	counts map[string]int
}

func newSampler(h slog.Handler, first, every int) slog.Handler {
	return sampler{h, first, every, &samplerState{counts: map[string]int{}}}
}

func (s sampler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		return s.Handler.Handle(ctx, r)
	}
	s.st.mu.Lock()
	if now := r.Time.Unix(); now != s.st.second {
		s.st.second, s.st.counts = now, map[string]int{}
	}
	s.st.counts[r.Message]++
	n := s.st.counts[r.Message]
	s.st.mu.Unlock()

	if n <= s.first {
		return s.Handler.Handle(ctx, r)
	}
	if (n-s.first)%s.every == 0 {
		r.AddAttrs(slog.Int("sampled_out", s.every-1))
		return s.Handler.Handle(ctx, r)
	}
	return nil
}
func (s sampler) WithAttrs(as []slog.Attr) slog.Handler {
	return sampler{s.Handler.WithAttrs(as), s.first, s.every, s.st}
}
func (s sampler) WithGroup(n string) slog.Handler {
	return sampler{s.Handler.WithGroup(n), s.first, s.every, s.st}
}

// ❌ BAD: random sampling of log lines in the code. You lose the ERROR
// lines too, and the lines of one request no longer agree: half a story
// per trace.
func badLogSampling(ctx context.Context, err error) {
	if rand.IntN(10) == 0 { // ❌ 90% of errors vanish
		slog.ErrorContext(ctx, "transfer failed", "error", err)
	}
}

// ═════════════════════════════════════════════════════════════════════════
// 3. TRACES
// ═════════════════════════════════════════════════════════════════════════
//
// A trace is a tree of spans for one request across services. Each span is
// one unit of work with a start, an end, attributes and a status.

// One tracer per package, named after it (the instrumentation scope).
var tracer = otel.Tracer("devfest/transfers")

// ─── 3.1 Starting and ending spans ───────────────────────────────────────

// ✅ GOOD: the pattern, every time.
//  1. Start from the ctx you were given, so the span gets the right parent.
//  2. defer span.End() on the next line.
//  3. Pass the NEW ctx to everything you call, so their spans nest.
func ReserveFunds(ctx context.Context, account string, kobo int64) error {
	ctx, span := tracer.Start(ctx, "ledger.reserve_funds")
	defer span.End()

	return postLedger(ctx, account, kobo) // ✅ child spans hang off this one
}

// ❌ BAD: three ways to break a trace.
func badSpans(ctx context.Context, account string) error {
	// ❌ context.Background(): a new root span. This work shows up as a
	//    separate trace, disconnected from the request that caused it.
	_, span := tracer.Start(context.Background(), "reserve_funds")

	// ❌ The new ctx is thrown away (the _ above), so postLedger's spans
	//    attach to the caller instead of to this span.
	if err := postLedger(ctx, account, 0); err != nil {
		return err // ❌ returns before span.End(): the span is never sent
	}
	span.End()
	return nil
}

// ─── 3.2 Span names ──────────────────────────────────────────────────────
//
// The span name is what you group and search by, so keep it low-
// cardinality. IDs go in attributes.
//
// ✅ GOOD                            ❌ BAD
//   POST /transfers/{id}               POST /transfers/8c1f2a9e
//   ledger.reserve_funds               reserve funds for acc_123 of 5000
//   SELECT accounts                    SELECT * FROM accounts WHERE id=$1 ...
//   provider.payout                    doStuff / handler / func1

// ─── 3.3 Attributes, events, errors and status ───────────────────────────

// ✅ GOOD
func CallProvider(ctx context.Context, provider string, t Transfer) error {
	ctx, span := tracer.Start(ctx, "provider.payout",
		trace.WithSpanKind(trace.SpanKindClient), // ✅ calls out of the process
		trace.WithAttributes(
			// ✅ IDs ARE fine here: spans are stored individually, not
			//    aggregated, so cardinality doesn't hurt.
			attribute.String("transfer.id", t.ID),
			attribute.Int64("transfer.amount_kobo", t.Kobo),
			attribute.String("payment.provider", provider),
		))
	defer span.End()

	for attempt := 1; attempt <= 3; attempt++ {
		err := payout(ctx, t)
		if err == nil {
			span.SetAttributes(attribute.Int("retry.attempts", attempt))
			return nil // status stays Unset, which means OK
		}
		// ✅ An event marks a moment inside the span, with its own fields.
		span.AddEvent("retry", trace.WithAttributes(
			attribute.Int("attempt", attempt),
			attribute.String("error.type", errorClass(err)),
		))
		if !errors.Is(err, ErrProviderTimeout) {
			// ✅ Both: RecordError keeps the details as an event,
			//    SetStatus marks the span failed (red in Grafana).
			span.RecordError(err)
			span.SetStatus(codes.Error, errorClass(err))
			span.SetAttributes(semconv.ErrorTypeKey.String(errorClass(err)))
			return err
		}
	}
	span.SetStatus(codes.Error, "timeout")
	return ErrProviderTimeout
}

func payout(ctx context.Context, t Transfer) error { return nil }

// ❌ BAD
func badCallProvider(ctx context.Context, t Transfer, cardNumber string) error {
	ctx, span := tracer.Start(ctx, "payout "+t.ID) // ❌ ID in the name
	defer span.End()

	span.SetAttributes(
		// ❌ traces are no safer a place for card numbers than logs
		attribute.String("card.number", cardNumber),
		attribute.String("request.body", fmt.Sprintf("%+v", t)), // ❌ big, and leaks the PIN
	)
	if err := payout(ctx, t); err != nil {
		span.RecordError(err) // ❌ no SetStatus: the span still shows as OK
		return err
	}
	// ❌ An expected outcome marked as an error: a 404 for "no such
	//    account" or a declined card isn't a failure of THIS service. Mark
	//    4xx as errors on the client side only (semconv says the same).
	span.SetStatus(codes.Error, "card declined")
	return nil
}

// ─── 3.4 Propagation between services ────────────────────────────────────

// ✅ GOOD: wrap the server and the client once, with otelhttp. Server
// spans read the incoming traceparent; client spans send it on. Metrics
// (http.server.request.duration) come for free.
func NewServer(mux *http.ServeMux) http.Handler {
	return otelhttp.NewHandler(mux, "transfers",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern // "POST /transfers/{id}"
			}
			return r.Method
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" // ✅ probes every 5 s are noise
		}),
	)
}

var httpClient = &http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport), // ✅ injects traceparent
	Timeout:   5 * time.Second,
}

func callLedger(ctx context.Context, url string) (*http.Response, error) {
	// ✅ NewRequestWithContext: the client span is a child of ctx's span.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	return httpClient.Do(req)
}

// ❌ BAD: the client call loses the context. The ledger starts a fresh
// trace, and the waterfall for this transfer stops at the transfers
// service.
func badCallLedger(url string) (*http.Response, error) {
	return http.Post(url, "application/json", nil) // ❌ no ctx, no traceparent
}

// ✅ GOOD: for anything that isn't HTTP (a queue, a gRPC metadata map, a
// database row for an outbox), inject and extract by hand.
func Publish(ctx context.Context, headers map[string]string) {
	ctx, span := tracer.Start(ctx, "transfers.payout publish",
		trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	// headers now has "traceparent": "00-<trace_id>-<span_id>-01"
}

// ─── 3.5 Goroutines, queues and span links ───────────────────────────────

// ✅ GOOD: a consumer continues the producer's trace when it handles one
// message, and LINKS to the producers when it handles a batch.
func Consume(headers map[string]string) {
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(headers))
	ctx, span := tracer.Start(ctx, "transfers.payout process",
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()
	_ = ctx
}

func ConsumeBatch(ctx context.Context, batch []map[string]string) {
	links := make([]trace.Link, 0, len(batch))
	for _, h := range batch {
		pctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(h))
		links = append(links, trace.LinkFromContext(pctx))
	}
	// ✅ One span for the batch, linked to each message's trace. Making
	//    it a child of one of them would hide the other 99.
	_, span := tracer.Start(ctx, "settlement.batch",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithLinks(links...),
		trace.WithAttributes(attribute.Int("messaging.batch.message_count", len(batch))))
	defer span.End()
}

// ✅ GOOD: goroutines get the parent ctx passed in explicitly.
func fanOut(ctx context.Context, accounts []string) {
	var wg sync.WaitGroup
	for _, acc := range accounts {
		wg.Go(func() {
			ctx, span := tracer.Start(ctx, "ledger.balance") // ✅ parent is the request
			defer span.End()
			_ = postLedger(ctx, acc, 0)
		})
	}
	wg.Wait()
}

// ❌ BAD: fire-and-forget work with the request's ctx. When the request
// returns, ctx is cancelled and the work dies half-way. And with
// context.Background() instead, the span is an orphan.
func badBackground(ctx context.Context) {
	go func() {
		_, span := tracer.Start(ctx, "send receipt") // ❌ ctx is cancelled soon
		defer span.End()
	}()
	// ✅ Fix: detach cancellation but keep the trace:
	//      bg := context.WithoutCancel(ctx)
}

// ─── 3.6 Sampling ────────────────────────────────────────────────────────
//
// At 2 000 requests a second, keeping every trace costs more than the
// service. Sampling keeps a share.
//
// HEAD sampling: decided when the trace starts, in the SDK. Cheap, but
// blind: it can't know yet that this request will be slow or fail.
//
// TAIL sampling: decided in the Collector after the whole trace arrives.
// Keeps every error and every slow trace, plus a share of the rest. Needs
// all spans of a trace to reach the same Collector instance.

// ✅ GOOD: ParentBased. The root service decides, every service after it
// follows the traceparent flag, so a trace is either complete or absent.
// Same as OTEL_TRACES_SAMPLER=parentbased_traceidratio
//
//	OTEL_TRACES_SAMPLER_ARG=0.1
func ProductionSampler(ratio float64) sdktrace.Sampler {
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// ✅ GOOD: tail sampling in the Collector (otelcol-contrib), so errors and
// slow requests are always kept. With this, the SDK can send everything
// (AlwaysSample) to a local Collector and the Collector decides:
//
//	processors:
//	  tail_sampling:
//	    decision_wait: 10s
//	    policies:
//	      - name: errors
//	        type: status_code
//	        status_code: {status_codes: [ERROR]}
//	      - name: slow
//	        type: latency
//	        latency: {threshold_ms: 500}
//	      - name: the-rest
//	        type: probabilistic
//	        probabilistic: {sampling_percentage: 5}
//
// Metrics are never sampled: rates and percentiles stay exact whatever
// share of traces you keep. That is why alerts are built on metrics.

// ❌ BAD: four ways sampling goes wrong.
func badSamplers() []sdktrace.Sampler {
	return []sdktrace.Sampler{
		// ❌ Every trace at full traffic: the tracing bill is bigger than the
		//    compute bill, and the exporter queue drops spans anyway.
		sdktrace.AlwaysSample(),

		// ❌ Ratio without ParentBased: each service rolls its own dice.
		//    At 10% and four services, a complete trace survives 0.01% of
		//    the time; the rest have holes in the middle.
		sdktrace.TraceIDRatioBased(0.1),

		// ❌ Off "to save money": the next incident has no traces at all.
		sdktrace.NeverSample(),
	}
	// ❌ And: sampling with rand in your own code (`if rand.Float64() < 0.1
	//    { start span }`). Children don't know, and nothing is consistent.
}

// ─── 3.7 Too many spans ──────────────────────────────────────────────────

// ✅ GOOD: one span for the loop, with the count, and events for the
// interesting items.
func reconcile(ctx context.Context, rows []string) {
	ctx, span := tracer.Start(ctx, "reconcile")
	defer span.End()
	span.SetAttributes(attribute.Int("rows", len(rows)))
	mismatches := 0
	for _, r := range rows {
		if !matches(ctx, r) {
			mismatches++
			if mismatches <= 10 {
				span.AddEvent("mismatch", trace.WithAttributes(attribute.String("row", r)))
			}
		}
	}
	span.SetAttributes(attribute.Int("mismatches", mismatches))
}

// ❌ BAD: a span per row. 50 000 spans in one trace: the UI can't render
// it, the exporter drops half, and the span limit (128 events, 128
// attributes, but no limit on children) won't save you.
func badReconcile(ctx context.Context, rows []string) {
	for _, r := range rows {
		_, span := tracer.Start(ctx, "check row "+r) // ❌ per row, and an ID in the name
		matches(ctx, r)
		span.End()
	}
}

func matches(ctx context.Context, row string) bool { return true }

// ═════════════════════════════════════════════════════════════════════════
// 4. ALL THREE TOGETHER
// ═════════════════════════════════════════════════════════════════════════

// TransferService is what a production handler looks like with all three
// signals. Note how little of it is telemetry: most of it comes from
// otelhttp, the middleware and the logger, once.
type TransferService struct {
	Logger *slog.Logger
}

// ✅ GOOD
//   - the span comes from otelhttp (NewServer); this adds attributes to it
//   - business metric with bounded attributes
//   - one log line at the edge, with trace_id, at the right level
//   - IDs on the span and the log, never on the metric
func (s *TransferService) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx) // the server span otelhttp started

	t, err := decode(r)
	if err != nil {
		var ve *ValidationError
		errors.As(err, &ve)
		s.Logger.InfoContext(ctx, "transfer rejected", slog.String("reason", ve.Field))
		transfersCreated.Add(ctx, 1, metric.WithAttributes(
			attribute.String("status", "rejected")))
		http.Error(w, err.Error(), http.StatusBadRequest) // 4xx: not a server error
		return
	}
	span.SetAttributes(attribute.String("transfer.id", t.ID))
	logger := s.Logger.With(slog.String("transfer_id", t.ID))

	start := time.Now()
	err = s.execute(ctx, t)
	recordTransfer(ctx, t.Kobo, "NGN", err)
	transferSize.Record(ctx, t.Kobo)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, errorClass(err))
		logger.ErrorContext(ctx, "transfer failed",
			slog.String("error", err.Error()),
			slog.String("error_class", errorClass(err)),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()))
		http.Error(w, "transfer failed", statusFor(err))
		return
	}
	logger.InfoContext(ctx, "transfer completed",
		slog.Any("transfer", t), // masked by Transfer.LogValue
		slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	w.WriteHeader(http.StatusCreated)
}

func (s *TransferService) execute(ctx context.Context, t Transfer) error {
	if err := ReserveFunds(ctx, string(t.From), t.Kobo); err != nil {
		return fmt.Errorf("reserve: %w", err)
	}
	if err := CallProvider(ctx, "paystack", t); err != nil {
		return fmt.Errorf("payout: %w", err)
	}
	return nil
}

func decode(r *http.Request) (Transfer, error) {
	kobo, err := strconv.ParseInt(r.FormValue("amount_kobo"), 10, 64)
	if err != nil || kobo <= 0 {
		return Transfer{}, &ValidationError{Field: "amount_kobo"}
	}
	return Transfer{ID: "tr_" + strconv.FormatInt(time.Now().UnixNano(), 36), Kobo: kobo,
		From: AccountNumber(r.FormValue("from")), To: AccountNumber(r.FormValue("to"))}, nil
}

func statusFor(err error) int {
	switch errorClass(err) {
	case "insufficient_funds":
		return http.StatusUnprocessableEntity
	case "timeout":
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// ❌ BAD: the same handler with the mistakes from this file. Every line
// compiles, ships and "works". None of it helps at 3 a.m.
func (s *TransferService) BadCreate(w http.ResponseWriter, r *http.Request) {
	log.Printf("got request %s %s from %s", r.Method, r.URL, r.RemoteAddr) // ❌ 2.2, 2.5

	// ❌ 3.1: new root span, request's trace is lost
	ctx, span := tracer.Start(context.Background(), "create transfer "+r.URL.Path) // ❌ 3.2

	// ❌ 1.2: instrument per request, user ID as an attribute
	c, _ := otel.Meter("m").Int64Counter("transfers_total") // ❌ 1.1
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("user", r.Header.Get("X-User-ID"))))

	t, err := decode(r)
	if err != nil {
		slog.Error("validation failed", "err", err) // ❌ 2.3: a client mistake as ERROR
		http.Error(w, err.Error(), 400)
		return // ❌ 3.1: span never ended
	}
	slog.Info("transfer", "body", fmt.Sprintf("%+v", t)) // ❌ 2.5: PIN in the logs

	if err := s.execute(ctx, t); err != nil {
		slog.Error(err.Error())         // ❌ 2.2, 2.4: no context, no fields, no trace_id
		span.RecordError(err)           // ❌ 3.3: no SetStatus
		http.Error(w, err.Error(), 500) // ❌ internal error text sent to the client
		return
	}
	span.End()
	g, _ := meter.Float64Gauge("latency") // ❌ 1.4: latency as a gauge
	g.Record(ctx, 0)
	w.WriteHeader(201)
}
