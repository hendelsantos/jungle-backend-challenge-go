package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
)

type wagerRequest struct {
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	PlayerID                       uuid.UUID     `json:"playerId"`
	WalletID                       uuid.UUID     `json:"walletId"`
	RoundID                        string        `json:"roundId"`
	GameID                         string        `json:"gameId"`
	Kind                           wagering.Kind `json:"kind"`
	Money                          money.Money   `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId,omitempty"`
}

type wagerResult struct {
	TransactionID    uuid.UUID            `json:"transactionId"`
	Status           wagering.Status      `json:"status"`
	Balance          *money.Money         `json:"balance,omitempty"`
	WalletVersion    int64                `json:"walletVersion,omitempty"`
	FailureCode      wagering.FailureCode `json:"failureCode,omitempty"`
	IdempotentReplay bool                 `json:"idempotentReplay"`
}

func (a API) processWager(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	var body wagerRequest
	if err := decodeRequest(w, r, &body); err != nil {
		a.fail(w, err, id)
		return
	}
	if body.ProviderID != id.Principal.ProviderID {
		writeError(w, 403, "FORBIDDEN", "provider does not match authenticated identity", id.CorrelationID.String())
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || strings.TrimSpace(keys[0]) == "" {
		a.fail(w, errInvalidJSON, id)
		return
	}
	result, err := a.Process.ProcessWager(r.Context(), command.ProcessWagerInput{
		ProviderID: id.Principal.ProviderID, ExternalTransactionID: body.ExternalTransactionID,
		IdempotencyKey: keys[0], PlayerID: body.PlayerID, WalletID: body.WalletID, RoundID: body.RoundID,
		GameID: body.GameID, Kind: body.Kind, Money: body.Money,
		ReferenceExternalTransactionID: body.ReferenceExternalTransactionID, CorrelationID: id.CorrelationID,
	})
	if err != nil {
		a.fail(w, err, id)
		return
	}
	response := wagerResult{TransactionID: result.TransactionID, Status: result.Status, WalletVersion: result.WalletVersion, FailureCode: result.FailureCode, IdempotentReplay: result.IdempotentReplay}
	if result.HasBalance {
		response.Balance = &result.Balance
	}
	status := http.StatusOK
	switch result.Status {
	case wagering.Pending, wagering.PendingReference:
		status = 202
	case wagering.Rejected:
		status = 422
	case wagering.Failed:
		status = 500
	}
	if a.Logger != nil {
		a.Logger.Info("wager request completed", "correlationId", id.CorrelationID, "providerId", id.Principal.ProviderID, "walletId", body.WalletID, "transactionId", result.TransactionID, "status", result.Status, "idempotentReplay", result.IdempotentReplay)
	}
	writeJSON(w, status, response)
}

func (a API) getWager(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	transactionID, err := pathUUID(r, "transactionID")
	if err != nil {
		a.fail(w, err, id)
		return
	}
	state, err := a.Wager.ByID(r.Context(), id.Principal.ProviderID, transactionID)
	if err != nil {
		a.fail(w, err, id)
		return
	}
	writeJSON(w, 200, transactionResponse(state))
}
func (a API) getWagerByExternalID(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	if r.PathValue("providerID") != id.Principal.ProviderID {
		writeError(w, 403, "FORBIDDEN", "provider does not match authenticated identity", id.CorrelationID.String())
		return
	}
	state, err := a.Wager.ByExternalID(r.Context(), id.Principal.ProviderID, r.PathValue("externalID"))
	if err != nil {
		a.fail(w, err, id)
		return
	}
	writeJSON(w, 200, transactionResponse(state))
}

type transactionView struct {
	TransactionID uuid.UUID `json:"transactionId"`
	wagerRequest
	Status                 wagering.Status      `json:"status"`
	Balance                *money.Money         `json:"balance,omitempty"`
	WalletVersion          int64                `json:"walletVersion,omitempty"`
	FailureCode            wagering.FailureCode `json:"failureCode,omitempty"`
	ReferenceTransactionID *uuid.UUID           `json:"referenceTransactionId,omitempty"`
	CreatedAt              time.Time            `json:"createdAt"`
	UpdatedAt              time.Time            `json:"updatedAt"`
}

func transactionResponse(s wagering.State) transactionView {
	result := transactionView{
		TransactionID: s.Request.ID,
		wagerRequest:  wagerRequest{ProviderID: s.Request.ProviderID, ExternalTransactionID: s.Request.ExternalTransactionID, PlayerID: s.Request.PlayerID, WalletID: s.Request.WalletID, RoundID: s.Request.RoundID, GameID: s.Request.GameID, Kind: s.Request.Kind, Money: s.Request.Money, ReferenceExternalTransactionID: s.Request.ReferenceExternalTransactionID},
		Status:        s.Status, FailureCode: s.FailureCode, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if s.HasResult {
		result.Balance = &s.Result.Balance
		result.WalletVersion = s.Result.WalletVersion
	}
	if s.ReferenceID != uuid.Nil {
		result.ReferenceTransactionID = &s.ReferenceID
	}
	return result
}
