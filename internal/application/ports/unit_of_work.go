// Package ports declares the contracts the application needs from its adapters.
// Interfaces appear here only when a use case already consumes them.
package ports

import "context"

// UnitOfWork guarantees that fn executes inside a single SQL transaction:
// it commits when fn returns nil and rolls back on error or cancellation.
// A nested WithinTx joins the surrounding transaction instead of opening a
// second one, so a use case and its helpers always share one commit.
type UnitOfWork interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
