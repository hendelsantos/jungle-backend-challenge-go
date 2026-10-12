package sqs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/jsonx"
)

// WagerTransactionRequested is the only envelope type the consumer accepts.
const WagerTransactionRequested = "WagerTransactionRequested"

// ErrInvalidEnvelope marks messages that can never be processed: broken JSON,
// wrong type, empty identity or an OPENING from an external producer. The
// consumer dead-letters them; they are never retried.
var ErrInvalidEnvelope = errors.New("message envelope is permanently invalid")

// Envelope is the challenge message contract. Decoding goes through the same
// strict boundary as HTTP: objects and strings only, no unknown fields, no
// duplicate keys — monetary values cannot pass through float64.
type Envelope struct {
	MessageID  string    `json:"messageId"`
	Type       string    `json:"type"`
	OccurredAt string    `json:"occurredAt"`
	Data       WagerData `json:"data"`
}

type WagerData struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	IdempotencyKey                 string      `json:"idempotencyKey"`
	PlayerID                       uuid.UUID   `json:"playerId"`
	WalletID                       uuid.UUID   `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

// ParseEnvelope decodes and validates one message body and converts it to the
// consume command input. The returned payload hash is the SHA-256 of the raw
// body: identical redeliveries hash identically, a rewritten body does not.
func ParseEnvelope(body []byte, consumerName string) (command.ConsumeWagerInput, error) {
	if len(body) > 64*1024 {
		return command.ConsumeWagerInput{}, fmt.Errorf("%w: body exceeds 64 KiB", ErrInvalidEnvelope)
	}
	var envelope Envelope
	if err := jsonx.Decode(body, &envelope); err != nil {
		return command.ConsumeWagerInput{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	if strings.TrimSpace(envelope.MessageID) == "" || len(envelope.MessageID) > 256 || strings.ContainsRune(envelope.MessageID, 0) || envelope.Type != WagerTransactionRequested {
		return command.ConsumeWagerInput{}, fmt.Errorf("%w: messageId is required and type must be %q",
			ErrInvalidEnvelope, WagerTransactionRequested)
	}
	if occurred, err := time.Parse(time.RFC3339, envelope.OccurredAt); err != nil || occurred.IsZero() {
		return command.ConsumeWagerInput{}, fmt.Errorf("%w: occurredAt must be RFC 3339", ErrInvalidEnvelope)
	}
	// OPENING is reserved for the internal wallet opening: an external producer
	// sending it is a contract violation, dead-lettered without evaluation.
	if envelope.Data.Kind == string(wagering.Opening) {
		return command.ConsumeWagerInput{}, fmt.Errorf("%w: OPENING is not accepted from producers", ErrInvalidEnvelope)
	}
	// Deterministic correlation per message: redeliveries keep one trace id.
	correlation := uuid.NewSHA1(uuid.NameSpaceOID, []byte("sqs:"+consumerName+":"+envelope.MessageID))
	return command.ConsumeWagerInput{
		ConsumerName: consumerName,
		MessageID:    envelope.MessageID,
		PayloadHash:  hashBody(body),
		Wager: command.ProcessWagerInput{
			ProviderID:                     envelope.Data.ProviderID,
			ExternalTransactionID:          envelope.Data.ExternalTransactionID,
			IdempotencyKey:                 envelope.Data.IdempotencyKey,
			PlayerID:                       envelope.Data.PlayerID,
			WalletID:                       envelope.Data.WalletID,
			RoundID:                        envelope.Data.RoundID,
			GameID:                         envelope.Data.GameID,
			Kind:                           wagering.Kind(envelope.Data.Kind),
			Money:                          envelope.Data.Money,
			ReferenceExternalTransactionID: envelope.Data.ReferenceExternalTransactionID,
			CorrelationID:                  correlation,
		},
	}, nil
}

func hashBody(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
