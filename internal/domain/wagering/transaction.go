package wagering

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

type WagerTransaction struct{ state State }

func NewExternal(request Request, at time.Time) (*WagerTransaction, error) {
	if request.Kind == Opening {
		return nil, ErrInvalidKind
	}
	return Rehydrate(State{Request: request, Status: Pending, CreatedAt: at, UpdatedAt: at})
}

// NewOpening accepts an identity supplied by the application and no external metadata.
// Persistence must enforce one OPENING per wallet and reuse its identity on retries.
// Zero is a valid domain amount, but the wallet creation use case must omit OPENING for zero.
func NewOpening(id, walletID, playerID uuid.UUID, amount money.Money, at time.Time) (*WagerTransaction, error) {
	return Rehydrate(State{
		Request: Request{ID: id, WalletID: walletID, PlayerID: playerID, Kind: Opening, Money: amount},
		Status:  Pending, CreatedAt: at, UpdatedAt: at,
	})
}

func Rehydrate(state State) (*WagerTransaction, error) {
	state.CreatedAt, state.UpdatedAt = state.CreatedAt.UTC(), state.UpdatedAt.UTC()
	tx := &WagerTransaction{state: state}
	if err := tx.Validate(); err != nil {
		return nil, err
	}
	return tx, nil
}

func (tx WagerTransaction) Snapshot() State          { return tx.state }
func (tx WagerTransaction) ID() uuid.UUID            { return tx.state.Request.ID }
func (tx WagerTransaction) Kind() Kind               { return tx.state.Request.Kind }
func (tx WagerTransaction) Status() Status           { return tx.state.Status }
func (tx WagerTransaction) FailureCode() FailureCode { return tx.state.FailureCode }
func (tx WagerTransaction) Result() (Result, bool)   { return tx.state.Result, tx.state.HasResult }

func (tx *WagerTransaction) Validate() error {
	if tx == nil {
		return ErrInvalidTransaction
	}
	s := tx.state
	if err := validateRequest(s.Request); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: %w", ErrInvalidTransaction, ErrInvalidTime)
	}
	if s.HasResult {
		if err := s.Result.Balance.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
		}
		if s.Result.Balance.MinorUnits() < 0 || s.Result.WalletVersion < 1 {
			return ErrInvalidTransaction
		}
	} else if s.Result != (Result{}) {
		return ErrInvalidTransaction
	}
	if s.ReferenceID != uuid.Nil && (s.Request.ReferenceExternalTransactionID == "" || s.ReferenceID == s.Request.ID) {
		return ErrInvalidTransaction
	}
	switch s.Status {
	case Pending, PendingReference:
		if s.HasResult || s.FailureCode != "" || s.ReferenceID != uuid.Nil {
			return ErrInvalidTransaction
		}
		if s.Status == PendingReference && s.Request.ReferenceExternalTransactionID == "" {
			return ErrInvalidTransaction
		}
	case Processed:
		if !s.HasResult || s.FailureCode != "" || s.Result.Balance.Currency() != s.Request.Money.Currency() {
			return ErrInvalidTransaction
		}
		if (s.Request.ReferenceExternalTransactionID != "") != (s.ReferenceID != uuid.Nil) {
			return ErrInvalidTransaction
		}
		if s.Request.Kind == Opening && (s.Result.Balance != s.Request.Money || s.Result.WalletVersion != 1) {
			return ErrInvalidTransaction
		}
	case Rejected:
		if !isRejectionCode(s.FailureCode) {
			return ErrInvalidTransaction
		}
	case Failed:
		if s.FailureCode != PermanentInfrastructureFailure || s.HasResult || s.ReferenceID != uuid.Nil {
			return ErrInvalidTransaction
		}
	default:
		return ErrInvalidTransaction
	}
	return nil
}

func validateRequest(r Request) error {
	if r.ID == uuid.Nil || r.WalletID == uuid.Nil || r.PlayerID == uuid.Nil {
		return ErrInvalidTransaction
	}
	if err := r.Money.Validate(); err != nil {
		return err
	}
	switch r.Kind {
	case Bet, Win, Refund, Rollback:
		if r.Money.MinorUnits() <= 0 {
			return ErrInvalidAmount
		}
	case Loss:
		if r.Money.MinorUnits() != 0 {
			return ErrInvalidAmount
		}
	case Opening:
		if r.Money.MinorUnits() < 0 {
			return ErrInvalidAmount
		}
	default:
		return ErrInvalidKind
	}
	metadata := []string{r.ProviderID, r.ExternalTransactionID, r.IdempotencyKey, r.PayloadHash, r.RoundID, r.GameID}
	for _, value := range metadata {
		if r.Kind == Opening {
			if value != "" {
				return ErrInvalidTransaction
			}
		} else if strings.TrimSpace(value) == "" {
			return ErrInvalidTransaction
		}
	}
	if r.Kind != Opening {
		decoded, err := hex.DecodeString(r.PayloadHash)
		if err != nil || len(decoded) != 32 || r.PayloadHash != strings.ToLower(r.PayloadHash) {
			return ErrInvalidTransaction
		}
	}
	ref := r.ReferenceExternalTransactionID
	if (r.Kind == Refund || r.Kind == Rollback) && strings.TrimSpace(ref) == "" {
		return ErrInvalidReference
	}
	if ref != "" {
		if strings.TrimSpace(ref) == "" || ref == r.ExternalTransactionID {
			return ErrInvalidReference
		}
		if r.Kind != Win && r.Kind != Refund && r.Kind != Rollback {
			return ErrInvalidReference
		}
	}
	return nil
}

func (tx *WagerTransaction) canTransition(at time.Time) error {
	if err := tx.Validate(); err != nil {
		return err
	}
	if tx.state.Status != Pending && tx.state.Status != PendingReference {
		return ErrInvalidTransition
	}
	if at.IsZero() || at.Before(tx.state.UpdatedAt) {
		return ErrInvalidTime
	}
	return nil
}

// Reject records a definitive business decision, e.g. reference TTL exhaustion.
// When no wallet was read, no financial result is invented.
func (tx *WagerTransaction) Reject(code FailureCode, at time.Time) error {
	if err := tx.canTransition(at); err != nil {
		return err
	}
	if !isRejectionCode(code) {
		return ErrInvalidTransition
	}
	next := tx.state
	next.Status, next.FailureCode, next.UpdatedAt = Rejected, code, at.UTC()
	return tx.replace(next)
}

// FailPermanent is reserved for diagnosed permanent infrastructure failures.
// Transient I/O errors must leave the transaction retryable, not call this method.
func (tx *WagerTransaction) FailPermanent(at time.Time) error {
	if err := tx.canTransition(at); err != nil {
		return err
	}
	next := tx.state
	next.Status, next.FailureCode, next.UpdatedAt = Failed, PermanentInfrastructureFailure, at.UTC()
	return tx.replace(next)
}

func (tx *WagerTransaction) replace(next State) error {
	validated, err := Rehydrate(next)
	if err != nil {
		return err
	}
	tx.state = validated.state
	return nil
}

func isRejectionCode(code FailureCode) bool {
	switch code {
	case InsufficientFunds, ReversalInsufficientFunds, ReferenceNotFound, ReferenceNotProcessed,
		ReferenceScopeMismatch, ReferenceAmountMismatch, InvalidReferenceKind, AlreadyReversed,
		WalletCurrencyMismatch, BalanceOverflow:
		return true
	default:
		return false
	}
}
