package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/events"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

// Repositories must refuse writes outside a UnitOfWork transaction; this is
// the in-code enforcement of "financial state never commits alone".
func TestRepositoriesRequireActiveTransaction(t *testing.T) {
	ctx := context.Background()
	operations := map[string]func() error{
		"insert wallet":       func() error { return NewWalletRepository().InsertWallet(ctx, wallet.State{}) },
		"insert wager":        func() error { return NewWagerTransactionRepository().InsertWagerTransaction(ctx, wagering.State{}) },
		"insert ledger entry": func() error { return NewLedgerRepository().InsertLedgerEntry(ctx, ledger.Entry{}) },
		"insert outbox event": func() error { return NewOutboxRepository().InsertEvent(ctx, events.Envelope{}) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			err := operation()
			if err == nil {
				t.Fatalf("%s accepted a context without transaction", name)
			}
			if !strings.Contains(err.Error(), "requires an active UnitOfWork transaction") {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
		})
	}
}
