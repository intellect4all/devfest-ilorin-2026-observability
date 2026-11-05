// Package telemetry wires OpenTelemetry into a service. Every signal goes
// over OTLP/HTTP to the Collector at OTEL_EXPORTER_OTLP_ENDPOINT.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Setup starts telemetry for service and returns a function that flushes
// and stops it.
func Setup(ctx context.Context, service string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(service)),
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}

	// Metrics: pushed every 5 s.
	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(5*time.Second))),
	)
	otel.SetMeterProvider(mp)

	// USE for the process itself: goroutines, memory, GC (runtime package)
	// and CPU time (below; the runtime package has none).
	if err := runtime.Start(); err != nil {
		return nil, err
	}
	if err := processCPU(); err != nil {
		return nil, err
	}

	// Traces: batched off the request path. Every request is kept, which is
	// right for a demo; production systems usually sample.
	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(time.Second)),
	)
	otel.SetTracerProvider(tp)

	// W3C traceparent between services. Forget this and every service
	// starts its own trace.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	// Logs: slog everywhere. slog.SetDefault also routes the standard log
	// package through it.
	logExp, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp, sdklog.WithExportInterval(time.Second))),
	)
	global.SetLoggerProvider(lp)
	slog.SetDefault(newLogger(service, lp))

	return func(ctx context.Context) error {
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx), lp.Shutdown(ctx))
	}, nil
}

// processCPU reports process.cpu.time from getrusage(2).
func processCPU() error {
	m := otel.Meter("devfest/telemetry")
	_, err := m.Float64ObservableCounter("process.cpu.time",
		metric.WithUnit("s"),
		metric.WithDescription("CPU time used by this process."),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			var ru syscall.Rusage
			if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
				return err
			}
			o.Observe(tv(ru.Utime), metric.WithAttributes(attribute.String("cpu.mode", "user")))
			o.Observe(tv(ru.Stime), metric.WithAttributes(attribute.String("cpu.mode", "system")))
			return nil
		}))
	return err
}

func tv(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }

// SkipHealthz keeps liveness probes out of metrics and traces.
func SkipHealthz(r *http.Request) bool { return r.URL.Path != "/healthz" }

// SpanName names server and client spans "POST /transfers".
func SpanName(_ string, r *http.Request) string { return r.Method + " " + r.URL.Path }
