package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	httpapi "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/http"
	oidcauth "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/oidc"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/postgres"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/query"
	"go.uber.org/fx"
)

func securityModule() fx.Option {
	return fx.Module("security", fx.Provide(newAuthenticator), fx.Invoke(registerBusinessRoutes))
}
func newAuthenticator(lifecycle fx.Lifecycle, cfg Config) (*oidcauth.Verifier, error) {
	verifier, err := oidcauth.NewVerifier(oidcauth.Config{Issuer: cfg.OIDCIssuer, Audience: cfg.OIDCAudience})
	if err != nil {
		return nil, err
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return verifier.Start(ctx)
		},
		OnStop: func(context.Context) error { verifier.Close(); return nil },
	})
	return verifier, nil
}
func registerBusinessRoutes(server *http.Server, auth *oidcauth.Verifier, create command.CreateWalletDeps, process command.ProcessWagerDeps, wallet query.GetWalletDeps, ledger query.ListLedgerDeps, reconcile query.ReconcileWalletDeps, wagers *postgres.WagerTransactionReader, logger *slog.Logger) {
	api := httpapi.API{Auth: auth, Create: create, Process: process, Wallet: wallet, Ledger: ledger, Reconcile: reconcile, Wager: query.GetWagerDeps{Wagers: wagers}, Logger: logger}
	api.Register(server.Handler.(*http.ServeMux))
}
