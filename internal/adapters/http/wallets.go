package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/ledger"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/money"
)

type walletResponse struct {
	ID       uuid.UUID   `json:"id"`
	PlayerID uuid.UUID   `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

func (a API) createWallet(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	var body struct {
		PlayerID       uuid.UUID   `json:"playerId"`
		InitialBalance money.Money `json:"initialBalance"`
	}
	if err := decodeRequest(w, r, &body); err != nil {
		a.fail(w, err, id)
		return
	}
	result, err := a.Create.CreateWallet(r.Context(), command.CreateWalletInput{PlayerID: body.PlayerID, InitialBalance: body.InitialBalance, CorrelationID: id.CorrelationID})
	if err != nil {
		a.fail(w, err, id)
		return
	}
	w.Header().Set("Location", "/wallets/"+result.WalletID.String())
	writeJSON(w, 201, walletResponse{ID: result.WalletID, PlayerID: result.PlayerID, Balance: result.Balance, Version: result.Version})
}
func (a API) getWallet(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	walletID, err := pathUUID(r, "walletID")
	if err != nil {
		a.fail(w, err, id)
		return
	}
	state, err := a.Wallet.GetWallet(r.Context(), walletID)
	if err != nil {
		a.fail(w, err, id)
		return
	}
	writeJSON(w, 200, walletResponse{ID: state.ID, PlayerID: state.PlayerID, Balance: state.Balance, Version: state.Version})
}

type ledgerResponse struct {
	ID            uuid.UUID        `json:"id"`
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	CreatedAt     time.Time        `json:"createdAt"`
}

func (a API) listLedger(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	walletID, err := pathUUID(r, "walletID")
	if err != nil {
		a.fail(w, err, id)
		return
	}
	limit := 0
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		a.fail(w, errInvalidJSON, id)
		return
	}
	for key, items := range values {
		if (key != "limit" && key != "cursor") || len(items) != 1 {
			a.fail(w, errInvalidJSON, id)
			return
		}
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			a.fail(w, errInvalidJSON, id)
			return
		}
	}
	page, err := a.Ledger.ListLedger(r.Context(), walletID, values.Get("cursor"), limit)
	if err != nil {
		a.fail(w, err, id)
		return
	}
	entries := make([]ledgerResponse, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, ledgerResponse{ID: e.ID(), WalletID: e.WalletID(), TransactionID: e.TransactionID(), Direction: e.Direction(), Money: e.Amount(), BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(), CreatedAt: e.CreatedAt()})
	}
	writeJSON(w, 200, struct {
		Entries    []ledgerResponse `json:"entries"`
		NextCursor string           `json:"nextCursor,omitempty"`
	}{entries, page.NextCursor})
}
func (a API) reconcileWallet(w http.ResponseWriter, r *http.Request, id requestIdentity) {
	walletID, err := pathUUID(r, "walletID")
	if err != nil {
		a.fail(w, err, id)
		return
	}
	result, err := a.Reconcile.ReconcileWallet(r.Context(), walletID)
	if err != nil {
		a.fail(w, err, id)
		return
	}
	if a.Fin != nil {
		a.Fin.Reconciliations.Inc()
		if !result.Consistent {
			a.Fin.Divergences.Inc()
			if a.Logger != nil {
				a.Logger.Warn("wallet reconciliation mismatch", "walletId", walletID, "correlationId", id.CorrelationID)
			}
		}
	}
	writeJSON(w, 200, struct {
		WalletID          uuid.UUID   `json:"walletId"`
		StoredBalance     money.Money `json:"storedBalance"`
		CalculatedBalance money.Money `json:"calculatedBalance"`
		Difference        money.Money `json:"difference"`
		Consistent        bool        `json:"consistent"`
		CheckedEntries    int64       `json:"checkedEntries"`
	}{result.WalletID, result.StoredBalance, result.CalculatedBalance, result.Difference, result.Consistent, result.CheckedEntries})
}
