package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

func brl(cents int64) money.Money {
	value, err := money.FromMinorUnits(cents, money.BRL)
	if err != nil {
		panic(err)
	}
	return value
}

var occurredAt = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type envelopeInput struct {
	eventID, correlationID, causationID uuid.UUID
	occurredAt                          time.Time
}

func validEnvelopeInput() envelopeInput {
	return envelopeInput{
		eventID:       uuid.New(),
		correlationID: uuid.New(),
		causationID:   uuid.New(),
		occurredAt:    occurredAt,
	}
}

func TestNewWagerTransactionProcessed(t *testing.T) {
	in := validEnvelopeInput()
	data := WagerTransactionProcessedData{
		TransactionID: uuid.New(),
		WalletID:      uuid.New(),
		PlayerID:      uuid.New(),
		Kind:          wagering.Bet,
		Money:         brl(2500),
	}

	envelope, err := NewWagerTransactionProcessed(in.eventID, in.correlationID, in.causationID, in.occurredAt, data)
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed(): %v", err)
	}
	if envelope.EventID() != in.eventID || envelope.CorrelationID() != in.correlationID {
		t.Fatalf("identity mismatch: %v/%v", envelope.EventID(), envelope.CorrelationID())
	}
	if envelope.EventType() != WagerTransactionProcessed {
		t.Fatalf("eventType: got %q", envelope.EventType())
	}
	if envelope.AggregateID() != data.TransactionID {
		t.Fatalf("aggregateId must be the transaction: got %v, want %v", envelope.AggregateID(), data.TransactionID)
	}
	if got, ok := envelope.CausationID(); !ok || got != in.causationID {
		t.Fatalf("causationId: got (%v, %v)", got, ok)
	}
	if !envelope.OccurredAt().Equal(in.occurredAt) || envelope.OccurredAt().Location() != time.UTC {
		t.Fatalf("occurredAt not stored in UTC: got %v", envelope.OccurredAt())
	}
	if envelope.Version() != 1 {
		t.Fatalf("version: got %d, want 1", envelope.Version())
	}
	if got, ok := envelope.Data().(WagerTransactionProcessedData); !ok || got != data {
		t.Fatalf("data: got %#v, want %#v", envelope.Data(), data)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}

	// OPENING is internal but still emits WagerTransactionProcessed when positive.
	data.Kind = wagering.Opening
	if _, err := NewWagerTransactionProcessed(in.eventID, in.correlationID, in.causationID, in.occurredAt, data); err != nil {
		t.Fatalf("NewWagerTransactionProcessed() with OPENING: %v", err)
	}

	// uuid.Nil causation is the documented way to express absence.
	withoutCausation, err := NewWagerTransactionProcessed(in.eventID, in.correlationID, uuid.Nil, in.occurredAt, data)
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed() without causation: %v", err)
	}
	if got, ok := withoutCausation.CausationID(); ok || got != uuid.Nil {
		t.Fatalf("causationId must be absent: got (%v, %v)", got, ok)
	}
}

func TestNewWagerTransactionProcessedRejectsInvalidInput(t *testing.T) {
	type input struct {
		envelopeInput
		data WagerTransactionProcessedData
	}
	valid := func() input {
		in := input{envelopeInput: validEnvelopeInput()}
		in.data = WagerTransactionProcessedData{
			TransactionID: uuid.New(),
			WalletID:      uuid.New(),
			PlayerID:      uuid.New(),
			Kind:          wagering.Bet,
			Money:         brl(2500),
		}
		return in
	}
	cases := []struct {
		name    string
		mutate  func(in *input)
		wantErr error
	}{
		{"nil event id", func(in *input) { in.eventID = uuid.Nil }, ErrInvalidEvent},
		{"nil correlation id", func(in *input) { in.correlationID = uuid.Nil }, ErrInvalidEvent},
		{"zero occurred at", func(in *input) { in.occurredAt = time.Time{} }, ErrInvalidEvent},
		{"nil transaction id", func(in *input) { in.data.TransactionID = uuid.Nil }, ErrInvalidEventData},
		{"nil wallet id", func(in *input) { in.data.WalletID = uuid.Nil }, ErrInvalidEventData},
		{"nil player id", func(in *input) { in.data.PlayerID = uuid.Nil }, ErrInvalidEventData},
		{"unknown kind", func(in *input) { in.data.Kind = wagering.Kind("GIFT") }, ErrInvalidEventData},
		{"uninitialized money", func(in *input) { in.data.Money = money.Money{} }, ErrInvalidEventData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.mutate(&in)

			envelope, err := NewWagerTransactionProcessed(in.eventID, in.correlationID, in.causationID, in.occurredAt, in.data)
			if err == nil {
				t.Fatalf("expected error %v, got %#v", tc.wantErr, envelope)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if envelope != (Envelope{}) {
				t.Fatalf("rejected events must return the zero value, got %#v", envelope)
			}
		})
	}
}

func TestNewWagerTransactionRejected(t *testing.T) {
	in := validEnvelopeInput()
	data := WagerTransactionRejectedData{
		TransactionID: uuid.New(),
		WalletID:      uuid.New(),
		PlayerID:      uuid.New(),
		Kind:          wagering.Bet,
		Money:         brl(8000),
		FailureCode:   wagering.InsufficientFunds,
	}

	envelope, err := NewWagerTransactionRejected(in.eventID, in.correlationID, in.causationID, in.occurredAt, data)
	if err != nil {
		t.Fatalf("NewWagerTransactionRejected(): %v", err)
	}
	if envelope.EventType() != WagerTransactionRejected || envelope.AggregateID() != data.TransactionID || envelope.Version() != 1 {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if envelope.Data() != data {
		t.Fatalf("data: got %#v", envelope.Data())
	}

	cases := []struct {
		name    string
		mutate  func(d *WagerTransactionRejectedData)
		wantErr error
	}{
		{"opening kind never rejects", func(d *WagerTransactionRejectedData) { d.Kind = wagering.Opening }, ErrInvalidEventData},
		{"unknown kind", func(d *WagerTransactionRejectedData) { d.Kind = wagering.Kind("GIFT") }, ErrInvalidEventData},
		{"blank failure code", func(d *WagerTransactionRejectedData) { d.FailureCode = wagering.FailureCode("  ") }, ErrInvalidEventData},
		{"empty failure code", func(d *WagerTransactionRejectedData) { d.FailureCode = "" }, ErrInvalidEventData},
		{"uninitialized money", func(d *WagerTransactionRejectedData) { d.Money = money.Money{} }, ErrInvalidEventData},
		{"nil transaction id", func(d *WagerTransactionRejectedData) { d.TransactionID = uuid.Nil }, ErrInvalidEventData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := data
			tc.mutate(&d)

			envelope, err := NewWagerTransactionRejected(in.eventID, in.correlationID, in.causationID, in.occurredAt, d)
			if err == nil {
				t.Fatalf("expected error %v, got %#v", tc.wantErr, envelope)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewWagerTransactionPendingReference(t *testing.T) {
	in := validEnvelopeInput()
	data := WagerTransactionPendingReferenceData{
		TransactionID:                  uuid.New(),
		WalletID:                       uuid.New(),
		PlayerID:                       uuid.New(),
		Kind:                           wagering.Refund,
		Money:                          brl(2500),
		ReferenceExternalTransactionID: "transaction-123",
	}

	envelope, err := NewWagerTransactionPendingReference(in.eventID, in.correlationID, in.causationID, in.occurredAt, data)
	if err != nil {
		t.Fatalf("NewWagerTransactionPendingReference(): %v", err)
	}
	if envelope.EventType() != WagerTransactionPendingReference || envelope.AggregateID() != data.TransactionID || envelope.Version() != 1 {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if envelope.Data() != data {
		t.Fatalf("data: got %#v", envelope.Data())
	}

	cases := []struct {
		name    string
		mutate  func(d *WagerTransactionPendingReferenceData)
		wantErr error
	}{
		{"kind without reference support", func(d *WagerTransactionPendingReferenceData) { d.Kind = wagering.Bet }, ErrInvalidEventData},
		{"loss cannot wait for reference", func(d *WagerTransactionPendingReferenceData) { d.Kind = wagering.Loss }, ErrInvalidEventData},
		{"opening cannot wait for reference", func(d *WagerTransactionPendingReferenceData) { d.Kind = wagering.Opening }, ErrInvalidEventData},
		{"blank reference", func(d *WagerTransactionPendingReferenceData) { d.ReferenceExternalTransactionID = "   " }, ErrInvalidEventData},
		{"empty reference", func(d *WagerTransactionPendingReferenceData) { d.ReferenceExternalTransactionID = "" }, ErrInvalidEventData},
		{"uninitialized money", func(d *WagerTransactionPendingReferenceData) { d.Money = money.Money{} }, ErrInvalidEventData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := data
			tc.mutate(&d)

			envelope, err := NewWagerTransactionPendingReference(in.eventID, in.correlationID, in.causationID, in.occurredAt, d)
			if err == nil {
				t.Fatalf("expected error %v, got %#v", tc.wantErr, envelope)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewWalletBalanceChanged(t *testing.T) {
	in := validEnvelopeInput()
	data := WalletBalanceChangedData{
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     ledger.Debit,
		Money:         brl(2500),
		BalanceBefore: brl(10000),
		BalanceAfter:  brl(7500),
		WalletVersion: 2,
	}

	envelope, err := NewWalletBalanceChanged(in.eventID, in.correlationID, in.causationID, in.occurredAt, data)
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged(): %v", err)
	}
	if envelope.EventType() != WalletBalanceChanged || envelope.AggregateID() != data.WalletID || envelope.Version() != 1 {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if envelope.Data() != data {
		t.Fatalf("data: got %#v", envelope.Data())
	}

	data.Direction = ledger.Credit
	data.BalanceAfter = brl(12500)
	if _, err := NewWalletBalanceChanged(in.eventID, in.correlationID, in.causationID, in.occurredAt, data); err != nil {
		t.Fatalf("NewWalletBalanceChanged() credit: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(d *WalletBalanceChangedData)
		wantErr error
	}{
		{"unknown direction", func(d *WalletBalanceChangedData) { d.Direction = ledger.Direction("debit") }, ErrInvalidEventData},
		{"zero money", func(d *WalletBalanceChangedData) { d.Money = brl(0) }, ErrInvalidEventData},
		{"negative money", func(d *WalletBalanceChangedData) { d.Money = brl(-2500) }, ErrInvalidEventData},
		{"uninitialized money", func(d *WalletBalanceChangedData) { d.Money = money.Money{} }, ErrInvalidEventData},
		{"negative balance before", func(d *WalletBalanceChangedData) { d.BalanceBefore = brl(-100) }, ErrInvalidEventData},
		{"negative balance after", func(d *WalletBalanceChangedData) { d.BalanceAfter = brl(-7500) }, ErrInvalidEventData},
		{"debit arithmetic mismatch", func(d *WalletBalanceChangedData) { d.BalanceAfter = brl(7000) }, ErrInvalidEventData},
		{"credit arithmetic mismatch", func(d *WalletBalanceChangedData) {
			d.Direction = ledger.Credit
			d.BalanceAfter = brl(7000)
		}, ErrInvalidEventData},
		{"money currency mismatch", func(d *WalletBalanceChangedData) {
			amount, err := money.FromMinorUnits(2500, money.USD)
			if err != nil {
				t.Fatalf("FromMinorUnits: %v", err)
			}
			d.Money = amount
		}, money.ErrCurrencyMismatch},
		{"credit overflow", func(d *WalletBalanceChangedData) {
			d.Direction = ledger.Credit
			d.BalanceBefore = brl(math.MaxInt64)
			d.Money = brl(100)
			d.BalanceAfter = brl(math.MaxInt64)
		}, money.ErrOverflow},
		{"zero wallet version", func(d *WalletBalanceChangedData) { d.WalletVersion = 0 }, ErrInvalidEventData},
		{"nil wallet id", func(d *WalletBalanceChangedData) { d.WalletID = uuid.Nil }, ErrInvalidEventData},
		{"nil transaction id", func(d *WalletBalanceChangedData) { d.TransactionID = uuid.Nil }, ErrInvalidEventData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := WalletBalanceChangedData{
				WalletID:      uuid.New(),
				TransactionID: uuid.New(),
				Direction:     ledger.Debit,
				Money:         brl(2500),
				BalanceBefore: brl(10000),
				BalanceAfter:  brl(7500),
				WalletVersion: 2,
			}
			tc.mutate(&d)

			envelope, err := NewWalletBalanceChanged(in.eventID, in.correlationID, in.causationID, in.occurredAt, d)
			if err == nil {
				t.Fatalf("expected error %v, got %#v", tc.wantErr, envelope)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRejectsInternallyInconsistentEnvelopes(t *testing.T) {
	processed := WagerTransactionProcessedData{
		TransactionID: uuid.New(),
		WalletID:      uuid.New(),
		PlayerID:      uuid.New(),
		Kind:          wagering.Bet,
		Money:         brl(2500),
	}
	balanced := WalletBalanceChangedData{
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     ledger.Debit,
		Money:         brl(2500),
		BalanceBefore: brl(10000),
		BalanceAfter:  brl(7500),
		WalletVersion: 2,
	}
	cases := []struct {
		name     string
		envelope Envelope
		wantErr  error
	}{
		{
			name: "event type disagrees with data",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: processed.TransactionID,
				occurredAt: occurredAt, version: 1, eventType: WalletBalanceChanged, data: processed},
			wantErr: ErrEventTypeMismatch,
		},
		{
			name: "unknown event type",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: processed.TransactionID,
				occurredAt: occurredAt, version: 1, eventType: Type("SomethingElse"), data: processed},
			wantErr: ErrEventTypeMismatch,
		},
		{
			name: "aggregate disagrees with data",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: uuid.New(),
				occurredAt: occurredAt, version: 1, eventType: WagerTransactionProcessed, data: processed},
			wantErr: ErrInvalidEvent,
		},
		{
			name: "wallet aggregate expected for balance change",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: balanced.TransactionID,
				occurredAt: occurredAt, version: 1, eventType: WalletBalanceChanged, data: balanced},
			wantErr: ErrInvalidEvent,
		},
		{
			name: "rejected aggregate must be the transaction",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: uuid.New(),
				occurredAt: occurredAt, version: 1, eventType: WagerTransactionRejected,
				data: WagerTransactionRejectedData{
					TransactionID: uuid.New(), WalletID: uuid.New(), PlayerID: uuid.New(),
					Kind: wagering.Bet, Money: brl(2500), FailureCode: wagering.InsufficientFunds,
				}},
			wantErr: ErrInvalidEvent,
		},
		{
			name: "pending reference aggregate must be the transaction",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: uuid.New(),
				occurredAt: occurredAt, version: 1, eventType: WagerTransactionPendingReference,
				data: WagerTransactionPendingReferenceData{
					TransactionID: uuid.New(), WalletID: uuid.New(), PlayerID: uuid.New(),
					Kind: wagering.Refund, Money: brl(2500), ReferenceExternalTransactionID: "transaction-123",
				}},
			wantErr: ErrInvalidEvent,
		},
		{
			name: "nil data",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: uuid.New(),
				occurredAt: occurredAt, version: 1, eventType: WagerTransactionProcessed},
			wantErr: ErrInvalidEvent,
		},
		{
			name: "zero version",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: balanced.WalletID,
				occurredAt: occurredAt, version: 0, eventType: WalletBalanceChanged, data: balanced},
			wantErr: ErrInvalidEvent,
		},
		{
			name: "causation flag without causation id",
			envelope: Envelope{eventID: uuid.New(), correlationID: uuid.New(), aggregateID: balanced.WalletID,
				occurredAt: occurredAt, version: 1, eventType: WalletBalanceChanged, data: balanced, hasCausation: true},
			wantErr: ErrInvalidEvent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.envelope.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(): got %v, want %v", err, tc.wantErr)
			}
			if _, err := json.Marshal(tc.envelope); !errors.Is(err, tc.wantErr) {
				t.Fatalf("json.Marshal(): got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestZeroValueIsInvalid(t *testing.T) {
	if err := (Envelope{}).Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Envelope{}.Validate(): got %v, want %v", err, ErrInvalidEvent)
	}
	if _, err := json.Marshal(Envelope{}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("json.Marshal(Envelope{}): got %v, want %v", err, ErrInvalidEvent)
	}
}

func TestWalletBalanceChangedJSONContract(t *testing.T) {
	eventID := uuid.MustParse("0192f298-345e-7e38-af88-e43f851a819d")
	walletID := uuid.MustParse("0192f291-27dd-7d3f-8071-5f8685deef37")
	transactionID := uuid.MustParse("0192f29a-40b1-7a52-9d3f-5d84a6f30b21")
	correlationID := uuid.MustParse("0192f29b-51c2-8b63-ae40-6e95b7a41c32")
	data := WalletBalanceChangedData{
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     ledger.Debit,
		Money:         brl(2500),
		BalanceBefore: brl(10000),
		BalanceAfter:  brl(7500),
		WalletVersion: 2,
	}

	envelope, err := NewWalletBalanceChanged(eventID, correlationID, uuid.Nil, occurredAt, data)
	if err != nil {
		t.Fatalf("NewWalletBalanceChanged(): %v", err)
	}
	got, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	want := fmt.Sprintf(
		`{"eventId":%q,"eventType":"WalletBalanceChanged","aggregateId":%q,"correlationId":%q,"occurredAt":"2026-10-12T12:00:00Z","version":1,"data":{"walletId":%q,"transactionId":%q,"direction":"DEBIT","money":{"amount":"25.00","currency":"BRL"},"balanceBefore":{"amount":"100.00","currency":"BRL"},"balanceAfter":{"amount":"75.00","currency":"BRL"},"walletVersion":2}}`,
		eventID, walletID, correlationID, walletID, transactionID,
	)
	if string(got) != want {
		t.Fatalf("json mismatch:\ngot  %s\nwant %s", got, want)
	}
}

func TestCausationIDJSON(t *testing.T) {
	eventID, correlationID, causationID := uuid.New(), uuid.New(), uuid.New()
	data := WagerTransactionProcessedData{
		TransactionID: uuid.New(),
		WalletID:      uuid.New(),
		PlayerID:      uuid.New(),
		Kind:          wagering.Bet,
		Money:         brl(2500),
	}

	withCausation, err := NewWagerTransactionProcessed(eventID, correlationID, causationID, occurredAt, data)
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed(): %v", err)
	}
	payload, err := json.Marshal(withCausation)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(): %v", err)
	}
	if got, _ := decoded["causationId"].(string); got != causationID.String() {
		t.Fatalf("causationId: got %q, want %q", got, causationID)
	}

	withoutCausation, err := NewWagerTransactionProcessed(eventID, correlationID, uuid.Nil, occurredAt, data)
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessed(): %v", err)
	}
	payload, err = json.Marshal(withoutCausation)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(): %v", err)
	}
	if _, ok := decoded["causationId"]; ok {
		t.Fatalf("causationId must be omitted when absent: %s", payload)
	}
	eventData, _ := decoded["data"].(map[string]any)
	if eventData == nil {
		t.Fatalf("data missing: %s", payload)
	}
	if got, _ := eventData["kind"].(string); got != "BET" {
		t.Fatalf("kind: got %q, want BET", got)
	}
	moneyPayload, _ := eventData["money"].(map[string]any)
	if moneyPayload == nil {
		t.Fatalf("money missing: %s", payload)
	}
	if got, _ := moneyPayload["amount"].(string); got != "25.00" {
		t.Fatalf("money amount must be a decimal string: got %q", got)
	}
	if got, _ := decoded["occurredAt"].(string); got != "2026-10-12T12:00:00Z" {
		t.Fatalf("occurredAt must be RFC 3339 UTC: got %q", got)
	}
}
