//go:build integration

// These tests require a real PostgreSQL instance; they are excluded from the
// default run and never use mocks. Provide the connection with:
//
//	TEST_DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable \
//		go test -tags=integration ./...
package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/testsupport"
)

var errProbe = errors.New("probe failure")

func newTestUnitOfWork(t *testing.T) (*UnitOfWork, *pgxpool.Pool) {
	t.Helper()
	pool := testsupport.PostgresPoolOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS uow_probe (id integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS uow_probe")
	})
	if _, err := pool.Exec(ctx, "TRUNCATE uow_probe"); err != nil {
		t.Fatalf("truncate probe table: %v", err)
	}
	return NewUnitOfWork(pool), pool
}

func countProbe(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM uow_probe").Scan(&count); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	return count
}

func TestPoolPingsRealDatabase(t *testing.T) {
	_, pool := newTestUnitOfWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestWithinTxCommits(t *testing.T) {
	uow, pool := newTestUnitOfWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		_, err := TxFromContext(ctx)
		if !err {
			t.Fatal("transaction must be available to the function through the context")
		}
		_, execErr := txExec(ctx, "INSERT INTO uow_probe (id) VALUES (1)")
		return execErr
	})
	if err != nil {
		t.Fatalf("WithinTx(): %v", err)
	}
	if got := countProbe(t, pool); got != 1 {
		t.Fatalf("committed rows: got %d, want 1", got)
	}
}

func TestWithinTxRollsBackOnError(t *testing.T) {
	uow, pool := newTestUnitOfWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		if _, execErr := txExec(ctx, "INSERT INTO uow_probe (id) VALUES (1)"); execErr != nil {
			return execErr
		}
		return errProbe
	})
	if !errors.Is(err, errProbe) {
		t.Fatalf("original error must be preserved: got %v", err)
	}
	if got := countProbe(t, pool); got != 0 {
		t.Fatalf("rolled back rows: got %d, want 0", got)
	}
}

func TestNestedWithinTxJoinsOuterTransaction(t *testing.T) {
	uow, pool := newTestUnitOfWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		return uow.WithinTx(ctx, func(ctx context.Context) error {
			_, err := txExec(ctx, "INSERT INTO uow_probe (id) VALUES (1)")
			return err
		})
	})
	if err != nil {
		t.Fatalf("nested WithinTx(): %v", err)
	}
	if got := countProbe(t, pool); got != 1 {
		t.Fatalf("nested commit must be the outer commit: got %d rows, want 1", got)
	}
}

func TestNestedFailureRollsBackOuterTransaction(t *testing.T) {
	uow, pool := newTestUnitOfWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		if _, execErr := txExec(ctx, "INSERT INTO uow_probe (id) VALUES (1)"); execErr != nil {
			return execErr
		}
		return uow.WithinTx(ctx, func(context.Context) error { return errProbe })
	})
	if !errors.Is(err, errProbe) {
		t.Fatalf("original error must be preserved: got %v", err)
	}
	if got := countProbe(t, pool); got != 0 {
		t.Fatalf("outer transaction must roll back: got %d rows, want 0", got)
	}
}

func TestWithinTxRollsBackWhenContextIsCancelled(t *testing.T) {
	uow, pool := newTestUnitOfWork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		if _, execErr := txExec(ctx, "INSERT INTO uow_probe (id) VALUES (1)"); execErr != nil {
			return execErr
		}
		cancel()
		return nil
	})
	cancel()
	if err == nil {
		t.Fatal("commit on a cancelled context must fail")
	}
	if got := countProbe(t, pool); got != 0 {
		t.Fatalf("no row may persist after a failed commit: got %d, want 0", got)
	}
}

func txExec(ctx context.Context, sql string) (int64, error) {
	tx, ok := TxFromContext(ctx)
	if !ok {
		return 0, fmt.Errorf("no transaction in context")
	}
	tag, err := tx.Exec(ctx, sql)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
