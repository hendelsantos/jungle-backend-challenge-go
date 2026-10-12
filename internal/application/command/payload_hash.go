package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

// canonicalPayload fields are declared in lexicographic JSON-key order, so
// json.Marshal produces the canonical document required by the challenge:
// sorted keys, UTF-8, no whitespace between tokens.
type canonicalPayload struct {
	Amount                         string        `json:"amount"`
	Currency                       string        `json:"currency"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	GameID                         string        `json:"gameId"`
	Kind                           wagering.Kind `json:"kind"`
	PlayerID                       uuid.UUID     `json:"playerId"`
	ProviderID                     string        `json:"providerId"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        string        `json:"roundId"`
	WalletID                       uuid.UUID     `json:"walletId"`
}

// payloadHash is the deterministic SHA-256 of the business content. The
// Idempotency-Key, transport metadata and timestamps are excluded by design,
// so HTTP and SQS compute the same digest for the same operation. The amount
// is normalized through FormatDecimal before hashing: "1", "1.0" and "1.00"
// are the same operation. An absent reference is omitted, not null.
func payloadHash(input ProcessWagerInput) (string, error) {
	amount, err := input.Money.FormatDecimal()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonicalPayload{
		Amount:                         amount,
		Currency:                       input.Money.Currency(),
		ExternalTransactionID:          input.ExternalTransactionID,
		GameID:                         input.GameID,
		Kind:                           input.Kind,
		PlayerID:                       input.PlayerID,
		ProviderID:                     input.ProviderID,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
		RoundID:                        input.RoundID,
		WalletID:                       input.WalletID,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
