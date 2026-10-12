// Package wagering implements transaction states and financial operation policies.
// It performs no I/O; persistence must commit all affected aggregates atomically.
package wagering

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

type FailureCode string

const (
	InsufficientFunds              FailureCode = "INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds      FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	ReferenceNotFound              FailureCode = "REFERENCE_NOT_FOUND"
	ReferenceNotProcessed          FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceScopeMismatch         FailureCode = "REFERENCE_SCOPE_MISMATCH"
	ReferenceAmountMismatch        FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	InvalidReferenceKind           FailureCode = "INVALID_REFERENCE_KIND"
	AlreadyReversed                FailureCode = "ALREADY_REVERSED"
	WalletCurrencyMismatch         FailureCode = "WALLET_CURRENCY_MISMATCH"
	BalanceOverflow                FailureCode = "BALANCE_OVERFLOW"
	PermanentInfrastructureFailure FailureCode = "PERMANENT_INFRASTRUCTURE_FAILURE"
)

var (
	ErrInvalidTransaction = errors.New("invalid wager transaction")
	ErrInvalidKind        = errors.New("invalid operation kind")
	ErrInvalidAmount      = errors.New("invalid operation amount")
	ErrInvalidReference   = errors.New("invalid external reference")
	ErrInvalidTransition  = errors.New("invalid transaction transition")
	ErrInvalidTime        = errors.New("invalid transaction timestamp")
	ErrWalletScope        = errors.New("wallet does not belong to this transaction")
)

// Request is copied on construction. PayloadHash is a lowercase SHA-256 hex
// digest computed by the application from canonical business fields, never trusted from a client.
type Request struct {
	ID                             uuid.UUID
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	Kind                           Kind
	Money                          money.Money
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
}

type Result struct {
	Balance       money.Money
	WalletVersion int64
}

// State is a detached snapshot for persistence. HasResult distinguishes an absent
// result from a real zero balance. It contains no mutable shared pointers or slices.
type State struct {
	Request     Request
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FailureCode FailureCode
	ReferenceID uuid.UUID
	Result      Result
	HasResult   bool
}

// Reference describes a lookup by (provider, external transaction ID).
// Compensated must come from durable state, coordinated by the same SQL transaction
// as processing. It means ANY successful direct REFUND or ROLLBACK already exists.
type Reference struct {
	Transaction *WagerTransaction
	Compensated bool
}
