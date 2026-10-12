// Command migrate applies the versioned SQL migrations with golang-migrate.
// It is meant for local operation and the delivery Compose job, never for the
// financial runtime: running it again is safe, and `down` is an explicit,
// destructive rollback that must be aimed at development databases.
//
// Usage:
//
//	migrate [-path migrations] up [N]
//	migrate [-path migrations] down N
//	migrate [-path migrations] version
//	migrate [-path migrations] force V
//
// The connection string comes from DATABASE_URL.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const usage = `usage: migrate [-path DIR] COMMAND [ARGUMENT]

Commands:
  up [N]     apply all pending migrations, or the next N
  down N     roll back the last N applied migrations (destructive)
  version    print the current schema version
  force V    record version V without running migrations

The connection string is read from DATABASE_URL.`

// pgx5Scheme converts a standard PostgreSQL URL into the scheme the
// golang-migrate pgx5 driver expects. Replacing the prefix avoids re-parsing
// the URL and preserves every query parameter, including sslmode.
func pgx5Scheme(connectionString string) (string, error) {
	switch {
	case strings.HasPrefix(connectionString, "pgx5://"):
		return connectionString, nil
	case strings.HasPrefix(connectionString, "postgres://"):
		return "pgx5://" + strings.TrimPrefix(connectionString, "postgres://"), nil
	case strings.HasPrefix(connectionString, "postgresql://"):
		return "pgx5://" + strings.TrimPrefix(connectionString, "postgresql://"), nil
	default:
		return "", fmt.Errorf("unsupported connection string: expected a postgres:// URL")
	}
}

// newMigrate opens the migration source over the filesystem and the database
// driver, reporting connectivity problems with the context-aware ping before
// golang-migrate opens its own connection on the pgx5 scheme.
func newMigrate(ctx context.Context, connectionString, dir string) (*migrate.Migrate, error) {
	dsn, err := pgx5Scheme(connectionString)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("migration path is empty")
	}
	d, err := iofs.New(os.DirFS(dir), ".")
	if err != nil {
		return nil, fmt.Errorf("open migrations: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// A short ping with the plain pgx/v5 stdlib driver fails fast and names
	// the real connection problem instead of the pgx5 wrapper URL.
	db, err := sql.Open("pgx/v5", strings.Replace(dsn, "pgx5", "postgres", 1))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close probe connection: %w", err)
	}

	migrator, err := migrate.NewWithSourceInstance("iofs", d, dsn)
	if err != nil {
		return nil, fmt.Errorf("initialize migrate: %w", err)
	}
	return migrator, nil
}

func execute(out io.Writer, connectionString string, args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("path", "migrations", "migration source directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New(usage)
	}
	command, argument := flags.Arg(0), flags.Arg(1)

	migrator, err := newMigrate(context.Background(), connectionString, *path)
	if err != nil {
		return err
	}
	defer func() {
		if _, err := migrator.Close(); err != nil {
			slog.Warn("migrate: close source", "error", err)
		}
	}()

	switch command {
	case "up":
		if argument == "" {
			return runSteps(migrator.Up)
		}
		n, err := positive("up", argument)
		if err != nil {
			return err
		}
		return runSteps(func() error { return migrator.Steps(n) })
	case "down":
		n, err := positive("down", argument)
		if err != nil {
			return err
		}
		return runSteps(func() error { return migrator.Steps(-n) })
	case "version":
		version, dirty, err := migrator.Version()
		switch {
		case errors.Is(err, migrate.ErrNilVersion):
			fmt.Fprintln(out, "none")
			return nil
		case err != nil:
			return fmt.Errorf("version: %w", err)
		case dirty:
			return fmt.Errorf("schema is dirty at version %d; inspect the database, then use force", version)
		}
		fmt.Fprintln(out, version)
		return nil
	case "force":
		if argument == "" {
			return fmt.Errorf("force: version argument required")
		}
		var version int
		if _, err := fmt.Sscan(argument, &version); err != nil || version < -1 {
			return fmt.Errorf("force: invalid version %q", argument)
		}
		return runSteps(func() error { return migrator.Force(version) })
	default:
		return fmt.Errorf("unknown command %q\n%s", command, usage)
	}
}

// runSteps treats ErrNoChange as success: both `up` at the target version and
// a `down` consuming the first migration report that the history is current.
func runSteps(step func() error) error {
	err := step()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, migrate.ErrNoChange):
		return nil
	default:
		return fmt.Errorf("migration failed; the database may hold the partially applied step: %w", err)
	}
}

func positive(command, text string) (int, error) {
	var n int
	if _, err := fmt.Sscan(text, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: expected a positive integer, got %q", command, text)
	}
	return n, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	connectionString := os.Getenv("DATABASE_URL")
	if connectionString == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(2)
	}
	if err := execute(os.Stdout, connectionString, os.Args[1:]); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
