package sqs

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

const validEnvelope = `{
	"messageId": "msg-123",
	"type": "WagerTransactionRequested",
	"occurredAt": "2026-09-08T12:00:00.000Z",
	"data": {
		"providerId": "provider-a",
		"externalTransactionId": "transaction-123",
		"idempotencyKey": "provider-a:transaction-123",
		"playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		"walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
		"roundId": "round-987",
		"gameId": "fortune-chimp",
		"kind": "BET",
		"money": {"amount": "25.00", "currency": "BRL"}
	}
}`

func TestParseEnvelopeBuildsConsumeInput(t *testing.T) {
	input, err := ParseEnvelope([]byte(validEnvelope), "wager-consumer")
	if err != nil {
		t.Fatal(err)
	}
	if input.ConsumerName != "wager-consumer" || input.MessageID != "msg-123" {
		t.Fatalf("input = %+v", input)
	}
	if len(input.PayloadHash) != 64 {
		t.Fatalf("payload hash = %q", input.PayloadHash)
	}
	wager := input.Wager
	if wager.ProviderID != "provider-a" || wager.ExternalTransactionID != "transaction-123" ||
		wager.IdempotencyKey != "provider-a:transaction-123" || wager.Kind != wagering.Bet ||
		wager.Money.MinorUnits() != 2500 || wager.RoundID != "round-987" {
		t.Fatalf("wager = %+v", wager)
	}
	if wager.CorrelationID == uuid.Nil {
		t.Fatal("correlation id missing")
	}
}

func TestParseEnvelopeCorrelationIsDeterministicPerMessage(t *testing.T) {
	first, err := ParseEnvelope([]byte(validEnvelope), "wager-consumer")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseEnvelope([]byte(validEnvelope), "wager-consumer")
	if err != nil {
		t.Fatal(err)
	}
	if first.Wager.CorrelationID != second.Wager.CorrelationID {
		t.Fatal("same message produced different correlation ids")
	}
	if first.PayloadHash != second.PayloadHash {
		t.Fatal("identical redelivery hashed differently")
	}
}

func TestParseEnvelopeRejectsPermanentViolations(t *testing.T) {
	cases := map[string]string{
		"broken json":           `{"messageId":`,
		"numeric amount":        strings.Replace(validEnvelope, `"amount": "25.00"`, `"amount": 25.00`, 1),
		"unknown field":         strings.Replace(validEnvelope, `"kind": "BET"`, `"kind": "BET", "extra": "x"`, 1),
		"wrong type":            strings.Replace(validEnvelope, `"WagerTransactionRequested"`, `"SomethingElse"`, 1),
		"empty message id":      strings.Replace(validEnvelope, `"msg-123"`, `""`, 1),
		"zero occurredAt":       strings.Replace(validEnvelope, `"2026-09-08T12:00:00.000Z"`, `"0001-01-01T00:00:00Z"`, 1),
		"blank identity":        strings.Replace(validEnvelope, `"msg-123"`, `"   "`, 1),
		"oversized body":        validEnvelope + strings.Repeat(" ", 64*1024),
		"bad occurredAt":        strings.Replace(validEnvelope, `"2026-09-08T12:00:00.000Z"`, `"yesterday"`, 1),
		"OPENING from producer": strings.Replace(validEnvelope, `"kind": "BET"`, `"kind": "OPENING"`, 1),
		"null kind":             strings.Replace(validEnvelope, `"kind": "BET"`, `"kind": null`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEnvelope([]byte(body), "wager-consumer"); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("err = %v, want ErrInvalidEnvelope", err)
			}
		})
	}
}

func TestParseEnvelopeReferenceIsOptionalButValidated(t *testing.T) {
	withReference := strings.Replace(validEnvelope, `"kind": "BET"`,
		`"kind": "REFUND", "referenceExternalTransactionId": "transaction-123-ref"`, 1)
	input, err := ParseEnvelope([]byte(withReference), "wager-consumer")
	if err != nil {
		t.Fatal(err)
	}
	if input.Wager.Kind != wagering.Refund || input.Wager.ReferenceExternalTransactionID != "transaction-123-ref" {
		t.Fatalf("wager = %+v", input.Wager)
	}
}
