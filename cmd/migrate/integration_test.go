//go:build integration

// The migration runner against a real PostgreSQL database and the real
// migration files: up applies the whole schema and records version 4, down
// removes it, and repeating up stays a no-op. A temporary database is created
// and dropped per run, so development data is never touched.
package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var emptyOut io.Writer = io.Discard

// migrationsDir resolves the repository migrations from this file location,
// because test binaries run with the package directory as working directory.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source file")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(root, "migrations")
}

// requireIntegrationURL returns an admin connection string from
// TEST_DATABASE_URL. This test never mocks the database and needs rights to
// create and drop temporary databases on the same server.
func requireIntegrationURL(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set: a real PostgreSQL server is required")
	}
	return admin
}

// createTemporaryDatabase makes an isolated universe for the runner and
// returns a connection string of the temporary database.
func createTemporaryDatabase(t *testing.T, admin string) string {
	t.Helper()
	temporary := "migrate_e2e_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	parsed, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	connection := *parsed
	connection.Path = "/" + temporary

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	t.Cleanup(func() {
		drop, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := conn.Exec(drop, "DROP DATABASE IF EXISTS "+pgx.Identifier{temporary}.Sanitize()); err != nil {
			t.Errorf("drop %s: %v", temporary, err)
		}
		_ = conn.Close(drop)
	})
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{temporary}.Sanitize()); err != nil {
		t.Fatalf("create temporary database: %v", err)
	}
	return connection.String()
}

func recordedVersion(t *testing.T, dsn string) (uint, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect temporary database: %v", err)
	}
	defer conn.Close(ctx)
	var version *int
	var dirty bool
	if err := conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		// A full rollback removes the row entirely; an empty history is clean.
		if err == pgx.ErrNoRows || errors.Is(err, pgx.ErrNoRows) || strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return 0, false
		}
		t.Fatalf("read schema_migrations: %v", err)
	}
	if version == nil {
		return 0, dirty
	}
	return uint(*version), dirty
}

func TestMigrateRunnerLifecycleIntegration(t *testing.T) {
	admin := requireIntegrationURL(t)
	dsn := createTemporaryDatabase(t, admin)
	source := migrationsDir(t)

	if err := execute(emptyOut, dsn, []string{"-path", source, "version"}); err != nil {
		t.Fatalf("version on fresh database: %v", err)
	}

	if err := execute(emptyOut, dsn, []string{"-path", source, "up"}); err != nil {
		t.Fatalf("up: %v", err)
	}
	version, dirty := recordedVersion(t, dsn)
	if version != 4 || dirty {
		t.Fatalf("after up: version=%d dirty=%v want version 4 clean", version, dirty)
	}

	// Re-running up is a deliberate no-op.
	if err := execute(emptyOut, dsn, []string{"-path", source, "up"}); err != nil {
		t.Fatalf("repeat up: %v", err)
	}

	if err := execute(emptyOut, dsn, []string{"-path", source, "down", "3"}); err != nil {
		t.Fatalf("down 3: %v", err)
	}
	if version, dirty := recordedVersion(t, dsn); version != 1 || dirty {
		t.Fatalf("after down 3: version=%d dirty=%v want version 1 clean", version, dirty)
	}

	if err := execute(emptyOut, dsn, []string{"-path", source, "up", "1"}); err != nil {
		t.Fatalf("up 1: %v", err)
	}
	if version, dirty := recordedVersion(t, dsn); version != 2 || dirty {
		t.Fatalf("after up 1: version=%d dirty=%v want version 2 clean", version, dirty)
	}

	if err := execute(emptyOut, dsn, []string{"-path", source, "down", "2"}); err != nil {
		t.Fatalf("down 2: %v", err)
	}
	if version, dirty := recordedVersion(t, dsn); version != 0 || dirty {
		t.Fatalf("after full rollback: version=%d dirty=%v want empty history clean", version, dirty)
	}
}
