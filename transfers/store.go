package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	createTransfersTable = `CREATE TABLE IF NOT EXISTS transfers (
		id              TEXT PRIMARY KEY,
		idempotency_key TEXT        NOT NULL UNIQUE,
		from_account    TEXT        NOT NULL,
		to_account      TEXT        NOT NULL,
		amount          BIGINT      NOT NULL,
		status          TEXT        NOT NULL,
		provider_ref    TEXT        NOT NULL DEFAULT '',
		created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	createTransfersIndex = `CREATE INDEX IF NOT EXISTS transfers_created_at_idx ON transfers (created_at)`

	insertTransfer = `INSERT INTO transfers (id, idempotency_key, from_account, to_account, amount, status, provider_ref, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	recentTransfersQuery = `SELECT id, idempotency_key, from_account, to_account, amount, status, provider_ref, created_at
		FROM transfers WHERE created_at > $1 ORDER BY created_at`
)

type store struct{ db *pgxpool.Pool }

func (s *store) migrate(ctx context.Context) error {
	for _, stmt := range []string{createTransfersTable, createTransfersIndex} {
		if _, err := s.db.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) save(ctx context.Context, t *transfer) error {
	_, err := s.db.Exec(ctx, insertTransfer,
		t.ID, t.Key, t.From, t.To, t.Amount, t.Status, t.ProviderRef, t.CreatedAt)
	return err
}

// recent loads every transfer created after since, oldest first.
func (s *store) recent(ctx context.Context, since time.Time) ([]*transfer, error) {
	rows, err := s.db.Query(ctx, recentTransfersQuery, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*transfer
	for rows.Next() {
		t := &transfer{}
		if err := rows.Scan(&t.ID, &t.Key, &t.From, &t.To, &t.Amount, &t.Status, &t.ProviderRef, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
