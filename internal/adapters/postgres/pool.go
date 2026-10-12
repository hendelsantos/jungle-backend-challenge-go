// Package postgres implements the application ports over pgx with explicit SQL.
// It has no Uber Fx dependency; composition and lifecycle live in bootstrap.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config carries the settings consumed by this adapter. URL accepts both the
// postgres:// scheme and the keyword/value DSN form; pgxpool.ParseConfig is
// the validator, so an invalid value fails application startup.
type Config struct {
	URL string
}

// NewPool parses the configuration and creates the pool without connecting:
// the first connection is established lazily. Lifetimes and health checks are
// set here so every environment gets the same defaults; connection counts stay
// with pgxpool until the Compose environment defines per-replica sizing.
func NewPool(cfg Config) (*pgxpool.Pool, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("postgres: DATABASE_URL is required")
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DATABASE_URL: %w", err)
	}
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	return pool, nil
}
