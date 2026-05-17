package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres holds the connection pool to PostgreSQL + PostGIS.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects to PostgreSQL and runs schema migrations.
// Returns nil if dsn is empty (PostGIS is optional).
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	if dsn == "" {
		return nil, nil
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}

	// Fix 8: larger pool to handle concurrent SaveRouteCalculation goroutines
	// alongside the async batch writer without queuing for a connection.
	// HealthCheckPeriod keeps the pool from accumulating stale connections.
	cfg.MaxConns = 25
	cfg.MinConns = 5
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	pg := &Postgres{pool: pool}
	if err := pg.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}

	return pg, nil
}

// Close releases all pool connections.
func (p *Postgres) Close() {
	if p != nil {
		p.pool.Close()
	}
}

// Pool exposes the underlying pool for repositories that need direct access.
func (p *Postgres) Pool() *pgxpool.Pool { return p.pool }
