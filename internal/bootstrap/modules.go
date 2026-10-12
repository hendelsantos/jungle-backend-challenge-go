// Package bootstrap is the composition root. Framework dependencies stay here.
package bootstrap

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	httpapi "github.com/hendelsantos/jungle-backend-go/internal/adapters/http"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func New() *fx.App {
	return fx.New(
		fx.Module("config", fx.Provide(LoadConfig)),
		fx.Module("logging", fx.Provide(newLogger)),
		fx.Module("http", fx.Provide(newHTTPServer), fx.Invoke(registerHTTP)),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
		fx.StartTimeout(15*time.Second),
		fx.StopTimeout(15*time.Second),
	)
}

func newLogger(cfg Config) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
}

func newHTTPServer(cfg Config, logger *slog.Logger) *http.Server {
	server := httpapi.NewServer(cfg.HTTPAddr)
	server.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	return server
}
