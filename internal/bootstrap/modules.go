// Package bootstrap is the composition root. Framework dependencies stay here.
package bootstrap

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	httpapi "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/http"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func New() *fx.App {
	options := []fx.Option{
		fx.Module("config", fx.Provide(LoadConfig)),
		fx.Module("logging", fx.Provide(newLogger)),
		fx.Module("http", fx.Provide(newHTTPServer)),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
		fx.StartTimeout(15 * time.Second),
		fx.StopTimeout(15 * time.Second),
	}
	if os.Getenv("DATABASE_URL") != "" {
		options = append(options, databaseModule(), applicationModule(), workersModule())
	}
	if os.Getenv("OIDC_ISSUER") != "" {
		options = append(options, securityModule())
	}
	// Start the listener only after database ping and OIDC discovery succeed.
	// Reverse shutdown order drains HTTP before closing its dependencies.
	options = append(options, fx.Invoke(registerHTTP))
	return fx.New(options...)
}

func newLogger(cfg Config) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
}

func newHTTPServer(cfg Config, logger *slog.Logger) *http.Server {
	server := httpapi.NewServer(cfg.HTTPAddr)
	server.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	return server
}
