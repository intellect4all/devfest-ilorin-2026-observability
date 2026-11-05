package telemetry

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DBPool reports how many of the pool's connections are in use: the USE
// "saturation" view of the database. When used reaches max, requests queue.
func DBPool(pool *pgxpool.Pool, name string) error {
	m := otel.Meter("devfest/telemetry")
	count, err := m.Int64ObservableUpDownCounter("db.client.connection.count",
		metric.WithUnit("{connection}"), metric.WithDescription("Connections in the pool, by state."))
	if err != nil {
		return err
	}
	limit, err := m.Int64ObservableUpDownCounter("db.client.connection.max",
		metric.WithUnit("{connection}"), metric.WithDescription("Most connections the pool may open."))
	if err != nil {
		return err
	}

	poolName := attribute.String("db.client.connection.pool.name", name)
	used := metric.WithAttributes(poolName, attribute.String("db.client.connection.state", "used"))
	idle := metric.WithAttributes(poolName, attribute.String("db.client.connection.state", "idle"))
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := pool.Stat()
		o.ObserveInt64(count, int64(s.AcquiredConns()), used)
		o.ObserveInt64(count, int64(s.IdleConns()), idle)
		o.ObserveInt64(limit, int64(s.MaxConns()), metric.WithAttributes(poolName))
		return nil
	}, count, limit)
	return err
}
