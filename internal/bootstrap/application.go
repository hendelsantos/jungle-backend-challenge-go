package bootstrap

import (
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/query"
)

// applicationModule is composed together with the database module: use cases
// depend on the UnitOfWork and the repositories bound to it.
func applicationModule() fx.Option {
	return fx.Module("application",
		fx.Provide(
			postgres.NewWalletRepository,
			postgres.NewWagerTransactionRepository,
			postgres.NewWagerTransactionReader,
			postgres.NewLedgerRepository,
			postgres.NewOutboxRepository,
			postgres.NewWalletReader,
			postgres.NewLedgerReader,
			postgres.NewReconciler,
			newCreateWalletDeps,
			newProcessWagerDeps,
			newGetWalletDeps,
			newListLedgerDeps,
			newReconcileWalletDeps,
		),
	)
}

func newCreateWalletDeps(
	uow *postgres.UnitOfWork,
	wallets *postgres.WalletRepository,
	wagers *postgres.WagerTransactionRepository,
	ledgerRepository *postgres.LedgerRepository,
	outbox *postgres.OutboxRepository,
) command.CreateWalletDeps {
	return command.CreateWalletDeps{
		UnitOfWork: uow,
		Wallets:    wallets,
		Wagers:     wagers,
		Ledger:     ledgerRepository,
		Outbox:     outbox,
		Now:        func() time.Time { return time.Now().UTC() },
		NewID:      newUUID,
	}
}

// newUUID prefers time-ordered v7 identities for sortable primary keys; on an
// entropy failure it falls back to v4 instead of failing the use case, since
// uniqueness is the actual requirement.
func newUUID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func newGetWalletDeps(wallets *postgres.WalletReader) query.GetWalletDeps {
	return query.GetWalletDeps{Wallets: wallets}
}

func newListLedgerDeps(wallets *postgres.WalletReader, ledger *postgres.LedgerReader) query.ListLedgerDeps {
	return query.ListLedgerDeps{Wallets: wallets, Ledger: ledger}
}

func newReconcileWalletDeps(reconciler *postgres.Reconciler) query.ReconcileWalletDeps {
	return query.ReconcileWalletDeps{Reconciliation: reconciler}
}

func newProcessWagerDeps(
	uow *postgres.UnitOfWork,
	wallets *postgres.WalletRepository,
	wagers *postgres.WagerTransactionRepository,
	lookups *postgres.WagerTransactionReader,
	ledgerRepository *postgres.LedgerRepository,
	outbox *postgres.OutboxRepository,
) command.ProcessWagerDeps {
	return command.ProcessWagerDeps{
		UnitOfWork: uow, Wallets: wallets, Wagers: wagers, WagerLookups: lookups,
		Ledger: ledgerRepository, Outbox: outbox, Now: time.Now, NewID: newUUID,
	}
}
