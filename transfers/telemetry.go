package main

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// initTracer is the four things every tracing setup needs.
func initTracer(ctx context.Context) (func(context.Context) error, error) {
	// 1. Exporter: where spans go. Reads OTEL_EXPORTER_OTLP_ENDPOINT (Jaeger, :4318).
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}

	// 2. Resource: who we are.
	res := resource.NewSchemaless(attribute.String("service.name", "transfers"))

	// 3. Processor: batch spans and export them off the request path.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// 4. Propagator: how context travels between services (W3C traceparent).
	//    Forget this and every service starts its own trace.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
