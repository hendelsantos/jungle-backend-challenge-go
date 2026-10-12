//go:build integration

// Package testsupport provisions real PostgreSQL for integration tests.
// It never mocks the database: tests skip when TEST_DATABASE_URL is absent,
// and the schema is the one from migrations/, applied once per test binary.
package testsupport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	poolErr  error
)

// PostgresPoolOrSkip returns the shared pool with migrations applied, skipping
// the test when no real database was provided.
func PostgresPoolOrSkip(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set: provide a real PostgreSQL connection string")
	}
	poolOnce.Do(func() {
		// A plain pool is enough here: this harness must not import the
		// postgres adapter, because that adapter's own tests import it too.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool, poolErr = pgxpool.New(ctx, url)
		if poolErr == nil {
			poolErr = applyMigrations(pool)
		}
	})
	if poolErr != nil {
		t.Fatalf("prepare test database: %v", poolErr)
	}
	return pool
}

// applyMigrations runs the real schema once per database: another test binary
// may already have applied it. The harness never resets data; tests rely on
// unique identities instead.
func applyMigrations(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var applied *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.wallets')").Scan(&applied); err != nil {
		return fmt.Errorf("check existing schema: %w", err)
	}
	if applied != nil {
		return nil
	}

	// Resolve the repository root from this file's location: the working
	// directory of a test binary belongs to whichever package is under test.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("locate testsupport source file")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	files, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no migration files found under %s", root)
	}
	for _, file := range files {
		script, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(script)); err != nil {
			return fmt.Errorf("apply %s: %w", filepath.Base(file), err)
		}
	}
	return nil
}
