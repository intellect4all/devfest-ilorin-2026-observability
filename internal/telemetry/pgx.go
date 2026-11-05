package telemetry

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// NewDBPool opens a pgx pool whose queries become client spans named like
// "SELECT entries", carrying db.query.text (the SQL, with $1 placeholders,
// never the values).
func NewDBPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Tracer = queryTracer{tracer: otel.Tracer("devfest/pgx")}
	return pgxpool.NewWithConfig(ctx, cfg)
}

type queryTracer struct{ tracer trace.Tracer }

type dbSpanKey struct{}

var tableRe = regexp.MustCompile(`(?i)\b(?:from|into|update|table)\s+([a-z_][a-z0-9_]*)`)

func (t queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// Only trace queries that belong to a request. Start-up work such as
	// migrations would otherwise show up as orphan one-span traces.
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	op, table := summarize(data.SQL)
	name := strings.TrimSpace(op + " " + table)
	ctx, span := t.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.DBSystemNamePostgreSQL,
			semconv.DBOperationName(op),
			semconv.DBCollectionName(table),
			semconv.DBQueryText(data.SQL),
		))
	return context.WithValue(ctx, dbSpanKey{}, span)
}

func (t queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(dbSpanKey{}).(trace.Span)
	if !ok {
		return
	}
	if data.Err != nil && !errors.Is(data.Err, pgx.ErrNoRows) {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

// summarize returns the SQL verb and the first table it touches.
func summarize(sql string) (op, table string) {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "QUERY", ""
	}
	op = strings.ToUpper(fields[0])
	if m := tableRe.FindStringSubmatch(sql); m != nil {
		table = m[1]
	}
	return op, table
}
