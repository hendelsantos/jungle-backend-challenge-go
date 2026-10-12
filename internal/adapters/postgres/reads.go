package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

var (
	_ ports.WalletReader = (*WalletReader)(nil)
	_ ports.LedgerReader = (*LedgerReader)(nil)
)

// executor lets a read join the active transaction when the context carries
// one, so future use cases can read consistently inside their unit of work.
type executor interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func executorFor(ctx context.Context, pool *pgxpool.Pool) executor {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return pool
}

type WalletReader struct {
	pool *pgxpool.Pool
}

func NewWalletReader(pool *pgxpool.Pool) *WalletReader {
	return &WalletReader{pool: pool}
}

func (r *WalletReader) FindWalletByID(ctx context.Context, id uuid.UUID) (wallet.State, error) {
	var (
		state        wallet.State
		currency     string
		balanceCents int64
	)
	err := executorFor(ctx, r.pool).QueryRow(ctx, `
		SELECT id, player_id, currency, balance_cents, version, created_at, updated_at
		FROM wallets
		WHERE id = $1`, id).
		Scan(&state.ID, &state.PlayerID, &currency, &balanceCents, &state.Version, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.State{}, ports.ErrWalletNotFound
	}
	if err != nil {
		return wallet.State{}, fmt.Errorf("postgres: find wallet: %w", err)
	}
	balance, err := money.FromMinorUnits(balanceCents, currency)
	if err != nil {
		return wallet.State{}, fmt.Errorf("postgres: wallet %s balance: %w", id, err)
	}
	state.Balance = balance
	// Reads revalidate the aggregate: a corrupted row must surface as an
	// error, never as a silently accepted wallet.
	rehydrated, err := wallet.Rehydrate(state)
	if err != nil {
		return wallet.State{}, fmt.Errorf("postgres: wallet %s violates invariants: %w", id, err)
	}
	return rehydrated.Snapshot(), nil
}

// ledgerCursor is the opaque pagination position: the stable (created_at, id)
// ordering of the entry that ended the previous page.
type ledgerCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func encodeLedgerCursor(at time.Time, id uuid.UUID) (string, error) {
	payload, err := json.Marshal(ledgerCursor{CreatedAt: at, ID: id})
	if err != nil {
		return "", fmt.Errorf("postgres: encode ledger cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeLedgerCursor(cursor string) (ledgerCursor, bool, error) {
	if cursor == "" {
		return ledgerCursor{}, false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return ledgerCursor{}, false, ports.ErrInvalidCursor
	}
	var decoded ledgerCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ledgerCursor{}, false, ports.ErrInvalidCursor
	}
	if decoded.CreatedAt.IsZero() || decoded.ID == uuid.Nil {
		return ledgerCursor{}, false, ports.ErrInvalidCursor
	}
	return decoded, true, nil
}

type LedgerReader struct {
	pool *pgxpool.Pool
}

func NewLedgerReader(pool *pgxpool.Pool) *LedgerReader {
	return &LedgerReader{pool: pool}
}

// ListEntriesByWallet pages with the stable (created_at, id) ordering, no
// OFFSET. It fetches limit+1 rows to detect a following page. Each row is
// rebuilt through the domain constructor, so a corrupted entry is an error.
func (r *LedgerReader) ListEntriesByWallet(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (ports.LedgerPage, error) {
	after, hasCursor, err := decodeLedgerCursor(cursor)
	if err != nil {
		return ports.LedgerPage{}, err
	}
	query := `
		SELECT e.id, e.wallet_id, e.transaction_id, e.direction, e.amount_cents,
		       e.balance_before_cents, e.balance_after_cents, e.created_at, w.currency
		FROM wallet_ledger_entries e
		JOIN wallets w ON w.id = e.wallet_id
		WHERE e.wallet_id = $1`
	args := []any{walletID}
	if hasCursor {
		query += fmt.Sprintf(" AND (e.created_at, e.id) > ($2, $3)")
		args = append(args, after.CreatedAt, after.ID)
	}
	query += " ORDER BY e.created_at, e.id LIMIT " + strconv.Itoa(limit+1)

	rows, err := executorFor(ctx, r.pool).Query(ctx, query, args...)
	if err != nil {
		return ports.LedgerPage{}, fmt.Errorf("postgres: list ledger entries: %w", err)
	}
	defer rows.Close()

	page := ports.LedgerPage{Entries: make([]ledger.Entry, 0, limit)}
	for rows.Next() {
		var (
			id, txWalletID, transactionID uuid.UUID
			direction, currency           string
			amount, before, afterCents    int64
			createdAt                     time.Time
		)
		if err := rows.Scan(&id, &txWalletID, &transactionID, &direction,
			&amount, &before, &afterCents, &createdAt, &currency); err != nil {
			return ports.LedgerPage{}, fmt.Errorf("postgres: scan ledger entry: %w", err)
		}
		// The ledger table has no currency column by design: the wallet of the
		// entry defines it, so the join supplies it for every row.
		amountMoney, err := money.FromMinorUnits(amount, currency)
		if err != nil {
			return ports.LedgerPage{}, fmt.Errorf("postgres: ledger %s amount: %w", id, err)
		}
		beforeMoney, err := money.FromMinorUnits(before, currency)
		if err != nil {
			return ports.LedgerPage{}, fmt.Errorf("postgres: ledger %s balance before: %w", id, err)
		}
		afterMoney, err := money.FromMinorUnits(afterCents, currency)
		if err != nil {
			return ports.LedgerPage{}, fmt.Errorf("postgres: ledger %s balance after: %w", id, err)
		}
		entry, err := ledger.New(id, txWalletID, transactionID, ledger.Direction(direction),
			amountMoney, beforeMoney, afterMoney, createdAt)
		if err != nil {
			return ports.LedgerPage{}, fmt.Errorf("postgres: ledger entry %s violates invariants: %w", id, err)
		}
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return ports.LedgerPage{}, fmt.Errorf("postgres: iterate ledger entries: %w", err)
	}
	if len(page.Entries) > limit {
		last := page.Entries[limit-1]
		next, err := encodeLedgerCursor(last.CreatedAt(), last.ID())
		if err != nil {
			return ports.LedgerPage{}, err
		}
		page.NextCursor = next
		page.Entries = page.Entries[:limit]
	}
	return page, nil
}
