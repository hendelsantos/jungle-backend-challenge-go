// Package events defines the typed domain events persisted to the transactional
// outbox and published only after commit. Envelopes are immutable values and
// carry no infrastructure dependencies.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

// Type identifies a concrete event contract; it is set by constructors only.
type Type string

const (
	WagerTransactionProcessed        Type = "WagerTransactionProcessed"
	WagerTransactionRejected         Type = "WagerTransactionRejected"
	WalletBalanceChanged             Type = "WalletBalanceChanged"
	WagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

// Each event type carries its own payload version, fixed by its constructor and
// bumped independently when a payload evolves.
const (
	wagerTransactionProcessedVersion        = 1
	wagerTransactionRejectedVersion         = 1
	walletBalanceChangedVersion             = 1
	wagerTransactionPendingReferenceVersion = 1
)

var (
	ErrInvalidEvent      = errors.New("invalid domain event")
	ErrInvalidEventData  = errors.New("invalid domain event data")
	ErrEventTypeMismatch = errors.New("event data does not match event type")
	ErrInvalidTime       = errors.New("invalid event timestamp")
)

// Data is implemented only by the payloads in this package; the unexported
// method keeps envelopes typed instead of accepting a generic map payload.
type Data interface{ eventData() }

// WagerTransactionProcessedData reports a concluded operation, including LOSS
// and the internal OPENING. The aggregate is the transaction. Amount rules were
// already enforced by the aggregates that emit the event.
type WagerTransactionProcessedData struct {
	TransactionID uuid.UUID     `json:"transactionId"`
	WalletID      uuid.UUID     `json:"walletId"`
	PlayerID      uuid.UUID     `json:"playerId"`
	Kind          wagering.Kind `json:"kind"`
	Money         money.Money   `json:"money"`
}

func (WagerTransactionProcessedData) eventData() {}

func (d WagerTransactionProcessedData) validate() error {
	if d.TransactionID == uuid.Nil || d.WalletID == uuid.Nil || d.PlayerID == uuid.Nil {
		return ErrInvalidEventData
	}
	if !validKind(d.Kind) {
		return ErrInvalidEventData
	}
	if err := d.Money.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEventData, err)
	}
	return nil
}

// WagerTransactionRejectedData reports a definitive business rejection. OPENING
// never rejects, so it is not a valid kind here.
type WagerTransactionRejectedData struct {
	TransactionID uuid.UUID            `json:"transactionId"`
	WalletID      uuid.UUID            `json:"walletId"`
	PlayerID      uuid.UUID            `json:"playerId"`
	Kind          wagering.Kind        `json:"kind"`
	Money         money.Money          `json:"money"`
	FailureCode   wagering.FailureCode `json:"failureCode"`
}

func (WagerTransactionRejectedData) eventData() {}

func (d WagerTransactionRejectedData) validate() error {
	if d.TransactionID == uuid.Nil || d.WalletID == uuid.Nil || d.PlayerID == uuid.Nil {
		return ErrInvalidEventData
	}
	if !externalKind(d.Kind) {
		return ErrInvalidEventData
	}
	if err := d.Money.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEventData, err)
	}
	if strings.TrimSpace(string(d.FailureCode)) == "" {
		return ErrInvalidEventData
	}
	return nil
}

// WagerTransactionPendingReferenceData reports that processing now waits for a
// reference. Only WIN, REFUND and ROLLBACK can carry an external reference.
type WagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID     `json:"transactionId"`
	WalletID                       uuid.UUID     `json:"walletId"`
	PlayerID                       uuid.UUID     `json:"playerId"`
	Kind                           wagering.Kind `json:"kind"`
	Money                          money.Money   `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId"`
}

func (WagerTransactionPendingReferenceData) eventData() {}

func (d WagerTransactionPendingReferenceData) validate() error {
	if d.TransactionID == uuid.Nil || d.WalletID == uuid.Nil || d.PlayerID == uuid.Nil {
		return ErrInvalidEventData
	}
	if !referenceWaitingKind(d.Kind) {
		return ErrInvalidEventData
	}
	if err := d.Money.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEventData, err)
	}
	if strings.TrimSpace(d.ReferenceExternalTransactionID) == "" {
		return ErrInvalidEventData
	}
	return nil
}

// WalletBalanceChangedData mirrors the ledger entry of an effective movement.
// The aggregate is the wallet. balanceAfter must equal balanceBefore ± money
// according to direction; the ledger entry identity does not belong to the
// event, so the invariant is checked here as well.
type WalletBalanceChangedData struct {
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}

func (WalletBalanceChangedData) eventData() {}

func (d WalletBalanceChangedData) validate() error {
	if d.WalletID == uuid.Nil || d.TransactionID == uuid.Nil {
		return ErrInvalidEventData
	}
	switch d.Direction {
	case ledger.Debit, ledger.Credit:
	default:
		return ErrInvalidEventData
	}
	for _, value := range []money.Money{d.Money, d.BalanceBefore, d.BalanceAfter} {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEventData, err)
		}
	}
	if d.Money.MinorUnits() <= 0 {
		return ErrInvalidEventData
	}
	if d.BalanceBefore.MinorUnits() < 0 || d.BalanceAfter.MinorUnits() < 0 {
		return ErrInvalidEventData
	}
	if d.WalletVersion < 1 {
		return ErrInvalidEventData
	}
	var expected money.Money
	var err error
	if d.Direction == ledger.Credit {
		expected, err = d.BalanceBefore.Add(d.Money)
	} else {
		expected, err = d.BalanceBefore.Sub(d.Money)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEventData, err)
	}
	equal, err := expected.Equal(d.BalanceAfter)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEventData, err)
	}
	if !equal {
		return ErrInvalidEventData
	}
	return nil
}

// Envelope is an immutable value; its zero value is invalid. CausationID is
// optional: uuid.Nil means the event has no causation. Version is the payload
// version of the event type. There is no UnmarshalJSON: the write path is the
// outbox, and deserialization belongs to consumers.
type Envelope struct {
	eventID       uuid.UUID
	eventType     Type
	aggregateID   uuid.UUID
	correlationID uuid.UUID
	causationID   uuid.UUID
	hasCausation  bool
	occurredAt    time.Time
	version       int
	data          Data
}

// NewWagerTransactionProcessed derives the aggregate from the data, so the
// envelope cannot disagree with it.
func NewWagerTransactionProcessed(eventID, correlationID, causationID uuid.UUID, occurredAt time.Time, data WagerTransactionProcessedData) (Envelope, error) {
	if err := data.validate(); err != nil {
		return Envelope{}, err
	}
	return newEnvelope(eventID, WagerTransactionProcessed, data.TransactionID, correlationID, causationID, occurredAt, wagerTransactionProcessedVersion, data)
}

func NewWagerTransactionRejected(eventID, correlationID, causationID uuid.UUID, occurredAt time.Time, data WagerTransactionRejectedData) (Envelope, error) {
	if err := data.validate(); err != nil {
		return Envelope{}, err
	}
	return newEnvelope(eventID, WagerTransactionRejected, data.TransactionID, correlationID, causationID, occurredAt, wagerTransactionRejectedVersion, data)
}

func NewWalletBalanceChanged(eventID, correlationID, causationID uuid.UUID, occurredAt time.Time, data WalletBalanceChangedData) (Envelope, error) {
	if err := data.validate(); err != nil {
		return Envelope{}, err
	}
	return newEnvelope(eventID, WalletBalanceChanged, data.WalletID, correlationID, causationID, occurredAt, walletBalanceChangedVersion, data)
}

func NewWagerTransactionPendingReference(eventID, correlationID, causationID uuid.UUID, occurredAt time.Time, data WagerTransactionPendingReferenceData) (Envelope, error) {
	if err := data.validate(); err != nil {
		return Envelope{}, err
	}
	return newEnvelope(eventID, WagerTransactionPendingReference, data.TransactionID, correlationID, causationID, occurredAt, wagerTransactionPendingReferenceVersion, data)
}

func newEnvelope(eventID uuid.UUID, eventType Type, aggregateID, correlationID, causationID uuid.UUID, occurredAt time.Time, version int, data Data) (Envelope, error) {
	e := Envelope{
		eventID:       eventID,
		eventType:     eventType,
		aggregateID:   aggregateID,
		correlationID: correlationID,
		occurredAt:    occurredAt.UTC(),
		version:       version,
		data:          data,
	}
	if causationID != uuid.Nil {
		e.causationID, e.hasCausation = causationID, true
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// Validate also rejects the zero value, unknown event types, envelopes whose
// data does not match their type and aggregates that disagree with the data.
func (e Envelope) Validate() error {
	if e.eventID == uuid.Nil || e.correlationID == uuid.Nil || e.aggregateID == uuid.Nil {
		return ErrInvalidEvent
	}
	if e.occurredAt.IsZero() {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidTime)
	}
	if e.version < 1 || (e.hasCausation && e.causationID == uuid.Nil) {
		return ErrInvalidEvent
	}
	switch d := e.data.(type) {
	case WagerTransactionProcessedData:
		if e.eventType != WagerTransactionProcessed {
			return ErrEventTypeMismatch
		}
		if e.aggregateID != d.TransactionID {
			return ErrInvalidEvent
		}
		return d.validate()
	case WagerTransactionRejectedData:
		if e.eventType != WagerTransactionRejected {
			return ErrEventTypeMismatch
		}
		if e.aggregateID != d.TransactionID {
			return ErrInvalidEvent
		}
		return d.validate()
	case WalletBalanceChangedData:
		if e.eventType != WalletBalanceChanged {
			return ErrEventTypeMismatch
		}
		if e.aggregateID != d.WalletID {
			return ErrInvalidEvent
		}
		return d.validate()
	case WagerTransactionPendingReferenceData:
		if e.eventType != WagerTransactionPendingReference {
			return ErrEventTypeMismatch
		}
		if e.aggregateID != d.TransactionID {
			return ErrInvalidEvent
		}
		return d.validate()
	case nil:
		return ErrInvalidEvent
	default:
		return ErrEventTypeMismatch
	}
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	payload := struct {
		EventID       uuid.UUID  `json:"eventId"`
		EventType     Type       `json:"eventType"`
		AggregateID   uuid.UUID  `json:"aggregateId"`
		CorrelationID uuid.UUID  `json:"correlationId"`
		CausationID   *uuid.UUID `json:"causationId,omitempty"`
		OccurredAt    time.Time  `json:"occurredAt"`
		Version       int        `json:"version"`
		Data          Data       `json:"data"`
	}{
		EventID:       e.eventID,
		EventType:     e.eventType,
		AggregateID:   e.aggregateID,
		CorrelationID: e.correlationID,
		OccurredAt:    e.occurredAt,
		Version:       e.version,
		Data:          e.data,
	}
	if e.hasCausation {
		causationID := e.causationID
		payload.CausationID = &causationID
	}
	return json.Marshal(payload)
}

// Accessors return values; an envelope cannot be mutated through them.
func (e Envelope) EventID() uuid.UUID             { return e.eventID }
func (e Envelope) EventType() Type                { return e.eventType }
func (e Envelope) AggregateID() uuid.UUID         { return e.aggregateID }
func (e Envelope) CorrelationID() uuid.UUID       { return e.correlationID }
func (e Envelope) CausationID() (uuid.UUID, bool) { return e.causationID, e.hasCausation }
func (e Envelope) OccurredAt() time.Time          { return e.occurredAt }
func (e Envelope) Version() int                   { return e.version }
func (e Envelope) Data() Data                     { return e.data }

func validKind(kind wagering.Kind) bool {
	switch kind {
	case wagering.Opening, wagering.Bet, wagering.Win, wagering.Loss, wagering.Refund, wagering.Rollback:
		return true
	default:
		return false
	}
}

func externalKind(kind wagering.Kind) bool {
	return kind != wagering.Opening && validKind(kind)
}

func referenceWaitingKind(kind wagering.Kind) bool {
	switch kind {
	case wagering.Win, wagering.Refund, wagering.Rollback:
		return true
	default:
		return false
	}
}
