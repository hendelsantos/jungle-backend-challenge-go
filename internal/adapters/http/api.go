package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/query"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/domain/wagering"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
)

type API struct {
	Auth      ports.Authenticator
	Fin       *observability.FinancialMetrics
	Create    command.CreateWalletDeps
	Process   command.ProcessWagerDeps
	Wallet    query.GetWalletDeps
	Ledger    query.ListLedgerDeps
	Reconcile query.ReconcileWalletDeps
	Wager     query.GetWagerDeps
	Logger    *slog.Logger
}

type requestIdentity struct {
	Principal     ports.Principal
	CorrelationID uuid.UUID
}
type endpoint func(http.ResponseWriter, *http.Request, requestIdentity)

func (a API) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /wallets", a.authorize("wallet:manage", a.createWallet))
	mux.HandleFunc("GET /wallets/{walletID}", a.authorize("wallet:read", a.getWallet))
	mux.HandleFunc("GET /wallets/{walletID}/ledger", a.authorize("wallet:read", a.listLedger))
	mux.HandleFunc("POST /wallets/{walletID}/reconciliation", a.authorize("wallet:manage", a.reconcileWallet))
	mux.HandleFunc("POST /wagering/transactions", a.authorize("provider", a.processWager))
	mux.HandleFunc("GET /wagering/transactions/{transactionID}", a.authorize("provider", a.getWager))
	mux.HandleFunc("GET /providers/{providerID}/wagering/transactions/{externalID}", a.authorize("provider", a.getWagerByExternalID))
}

func (a API) authorize(role string, next endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		correlation := uuid.New()
		if value := r.Header.Get("X-Correlation-ID"); value != "" {
			parsed, err := uuid.Parse(value)
			if err != nil || parsed == uuid.Nil {
				writeError(w, 400, "INVALID_CORRELATION_ID", "X-Correlation-ID must be a non-zero UUID", correlation.String())
				return
			}
			correlation = parsed
		}
		w.Header().Set("X-Correlation-ID", correlation.String())
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		headers := r.Header.Values("Authorization")
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(headers) != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || len(fields[1]) > 16<<10 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, 401, "UNAUTHENTICATED", "valid bearer token required", correlation.String())
			return
		}
		if a.Auth == nil {
			writeError(w, 503, "UNAVAILABLE", "authentication unavailable", correlation.String())
			return
		}
		principal, err := a.Auth.Authenticate(ctx, fields[1])
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeError(w, 401, "UNAUTHENTICATED", "invalid or expired access token", correlation.String())
			return
		}
		if !principal.HasRole(role) || (role == "provider" && strings.TrimSpace(principal.ProviderID) == "") {
			writeError(w, 403, "FORBIDDEN", "permission denied", correlation.String())
			return
		}
		next(w, r, requestIdentity{Principal: principal, CorrelationID: correlation})
	}
}

func (a API) fail(w http.ResponseWriter, err error, id requestIdentity) {
	status, code, message := 503, "UNAVAILABLE", "operation unavailable; retry with the same identity and content"
	switch {
	case errors.Is(err, ports.ErrWalletNotFound), errors.Is(err, ports.ErrWagerTransactionNotFound):
		status, code, message = 404, "NOT_FOUND", "resource not found"
	case errors.Is(err, ports.ErrWalletAlreadyExists), errors.Is(err, ports.ErrIdempotencyConflict):
		status, code, message = 409, "CONFLICT", "operation identity conflicts with existing data"
	case errors.Is(err, errInvalidJSON), errors.Is(err, command.ErrInvalidInput), errors.Is(err, command.ErrInvalidKind), errors.Is(err, command.ErrInvalidPlayerID), errors.Is(err, command.ErrInvalidCorrelation), errors.Is(err, command.ErrInvalidAmount), errors.Is(err, command.ErrCurrencyNotAccepted), errors.Is(err, wagering.ErrInvalidTransaction), errors.Is(err, wagering.ErrInvalidKind), errors.Is(err, wagering.ErrInvalidAmount), errors.Is(err, wagering.ErrInvalidReference), errors.Is(err, query.ErrInvalidWalletID), errors.Is(err, query.ErrInvalidWagerQuery), errors.Is(err, query.ErrInvalidLimit), errors.Is(err, ports.ErrInvalidCursor):
		status, code, message = 400, "INVALID_REQUEST", "request does not match the contract"
	case errors.Is(err, wagering.ErrWalletScope):
		status, code, message = 400, "INVALID_WALLET_SCOPE", "player and wallet must match"
	case errors.Is(err, command.ErrIncompleteDeps), errors.Is(err, query.ErrIncompleteDeps):
		status, code, message = 500, "INTERNAL_ERROR", "operation could not be completed"
	}
	if status >= 500 && a.Logger != nil {
		a.Logger.Error("financial request failed", "correlationId", id.CorrelationID, "providerId", id.Principal.ProviderID, "code", code)
	}
	writeError(w, status, code, message, id.CorrelationID.String())
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errInvalidJSON
	}
	return id, nil
}
