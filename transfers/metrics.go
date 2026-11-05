package main

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("devfest/transfers")

var (
	// transfersProcessed is the business view: how many transfers ended how.
	// Its only label is status, which has a handful of values. Never label a
	// metric with account or transfer IDs: those go in traces and logs.
	transfersProcessed, _ = meter.Int64Counter("transfers.processed",
		metric.WithUnit("{transfer}"),
		metric.WithDescription("Transfers handled, by outcome."))

	// payoutDuration times the call to the external provider. A histogram,
	// not an average: it gives p50, p99 and the shape.
	payoutDuration, _ = meter.Float64Histogram("payout.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of payout calls to the provider, by outcome."),
		metric.WithExplicitBucketBoundaries(.01, .025, .05, .075, .1, .15, .2, .3, .5, 1, 2, 5))
)
