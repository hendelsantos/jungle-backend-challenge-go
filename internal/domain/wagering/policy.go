package wagering

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wallet"
)

// Process evaluates a pending transaction and changes its state to PROCESSED,
// REJECTED or PENDING_REFERENCE. Business rejection is a successful evaluation
// (nil error); consult Status and FailureCode to distinguish outcomes.
// Errors leave BOTH the wallet and transaction unchanged.
// The application must persist their snapshots, ledger and events in one SQL commit.
func (tx *WagerTransaction) Process(w *wallet.Wallet, reference Reference, at time.Time) error {
	if err := tx.canTransition(at); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	r := tx.state.Request
	if r.WalletID != w.ID() || r.PlayerID != w.PlayerID() {
		return ErrWalletScope
	}
	next := tx.state
	next.UpdatedAt = at.UTC()
	status, code, refID, err := tx.evaluateReference(w, reference)
	if err != nil {
		return err
	}
	if status == PendingReference {
		next.Status = PendingReference
		return tx.replace(next)
	}
	// Work on an isolated copy so a later transaction validation cannot partially
	// change the caller's wallet. No goroutine shares these aggregates.
	candidate, err := wallet.Rehydrate(w.Snapshot())
	if err != nil {
		return err
	}
	if status == Processed {
		switch r.Kind {
		case Opening:
			// New wallet already holds its initial balance; never credit it a second time.
			if candidate.Version() != 1 || candidate.Balance() != r.Money || !candidate.CreatedAt().Equal(tx.state.CreatedAt) {
				return ErrWalletScope
			}
		case Bet:
			err = candidate.Debit(r.Money, at)
		case Win, Refund:
			err = candidate.Credit(r.Money, at)
		case Rollback:
			if reference.Transaction.Kind() == Bet {
				err = candidate.Credit(r.Money, at)
			} else {
				err = candidate.Debit(r.Money, at)
			}
		case Loss:
			// No movement and no wallet version change.
		}
		switch {
		case errors.Is(err, wallet.ErrInsufficientFunds):
			status, code = Rejected, InsufficientFunds
			if r.Kind == Rollback {
				code = ReversalInsufficientFunds
			}
		case errors.Is(err, money.ErrOverflow):
			status, code = Rejected, BalanceOverflow
		case err != nil:
			return err
		}
	}
	next.Status, next.FailureCode, next.ReferenceID = status, code, refID
	next.HasResult = true
	next.Result = Result{Balance: candidate.Balance(), WalletVersion: candidate.Version()}
	if err := tx.replace(next); err != nil {
		return err
	}
	if status == Processed {
		*w = *candidate
	}
	return nil
}

func (tx *WagerTransaction) evaluateReference(w *wallet.Wallet, reference Reference) (Status, FailureCode, uuid.UUID, error) {
	r := tx.state.Request
	if w.Currency() != r.Money.Currency() {
		return Rejected, WalletCurrencyMismatch, uuid.Nil, nil
	}
	if r.ReferenceExternalTransactionID == "" {
		return Processed, "", uuid.Nil, nil
	}
	ref := reference.Transaction
	if ref == nil {
		return PendingReference, "", uuid.Nil, nil
	}
	if err := ref.Validate(); err != nil {
		return "", "", uuid.Nil, err
	}
	s := ref.state.Request
	if s.ID == r.ID || s.ExternalTransactionID != r.ReferenceExternalTransactionID ||
		s.ProviderID != r.ProviderID || s.PlayerID != r.PlayerID || s.WalletID != r.WalletID ||
		s.Money.Currency() != r.Money.Currency() || s.RoundID != r.RoundID {
		return Rejected, ReferenceScopeMismatch, uuid.Nil, nil
	}
	if (r.Kind == Win || r.Kind == Refund) && s.Kind != Bet ||
		r.Kind == Rollback && s.Kind != Bet && s.Kind != Win && s.Kind != Refund {
		return Rejected, InvalidReferenceKind, s.ID, nil
	}
	if ref.Status() == Pending || ref.Status() == PendingReference {
		return PendingReference, "", uuid.Nil, nil
	}
	if ref.Status() != Processed {
		return Rejected, ReferenceNotProcessed, s.ID, nil
	}
	if r.Kind == Refund || r.Kind == Rollback {
		if r.Money != s.Money {
			return Rejected, ReferenceAmountMismatch, s.ID, nil
		}
		if reference.Compensated {
			return Rejected, AlreadyReversed, s.ID, nil
		}
	}
	return Processed, "", s.ID, nil
}
