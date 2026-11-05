package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// >>> STEP2
var tracer = otel.Tracer("transfers")

// <<< STEP2

const (
	createTransfersTable = `CREATE TABLE IF NOT EXISTS transfers (
		id              TEXT PRIMARY KEY,
		idempotency_key TEXT UNIQUE,
		from_account    TEXT        NOT NULL,
		to_account      TEXT        NOT NULL,
		amount          BIGINT      NOT NULL,
		status          TEXT        NOT NULL,
		provider_ref    TEXT        NOT NULL DEFAULT '',
		created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	lookupQuery = `SELECT id, status FROM transfers WHERE idempotency_key = $1`
	insertQuery = `INSERT INTO transfers (id, idempotency_key, from_account, to_account, amount, status, provider_ref)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
)

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, createTransfersTable)
	return err
}

// findByIdempotencyKey returns a transfer already made with this key, if any.
func (a *app) findByIdempotencyKey(ctx context.Context, key string) (id, status string, found bool, err error) {
	// >>> STEP2
	ctx, span := tracer.Start(ctx, "transfers.idempotency_lookup",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.query.text", lookupQuery),
		),
	)
	defer span.End()
	// <<< STEP2

	err = a.db.QueryRow(ctx, lookupQuery, key).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return id, status, true, nil
}

func (a *app) insertTransfer(ctx context.Context, id, key string, req transferRequest, status, ref string) error {
	var k any // NULL when no Idempotency-Key was sent
	if key != "" {
		k = key
	}
	_, err := a.db.Exec(ctx, insertQuery, id, k, req.From, req.To, req.Amount, status, ref)
	return err
}
