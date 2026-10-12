package postgres

import (
	"errors"
	"testing"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		code, constraint string
		retry            bool
	}{
		{"40P01", "", true}, {"40001", "", true},
		{"23505", "wager_transactions_provider_idempotency_uidx", true},
		{"23505", "wager_transactions_provider_external_uidx", true},
		{"23505", "outbox_events_pkey", false},
		{"23505", "wager_transactions_single_compensation_uidx", false},
		{"23514", "", false}, {"08006", "", false},
	} {
		t.Run(tc.code+tc.constraint, func(t *testing.T) {
			cause := &pgconn.PgError{Code: tc.code, ConstraintName: tc.constraint}
			result := markRetryable(cause)
			if errors.Is(result, ports.ErrRetryableTransaction) != tc.retry || !errors.Is(result, cause) {
				t.Fatalf("classification: %v", result)
			}
		})
	}
}
