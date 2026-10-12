package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"
)

func registerHTTP(lifecycle fx.Lifecycle, server *http.Server, logger *slog.Logger, shutdown fx.Shutdowner) {
	serveDone := make(chan struct{})
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var config net.ListenConfig
			listener, err := config.Listen(ctx, "tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("listen HTTP: %w", err)
			}
			logger.Info("HTTP server listening", "address", listener.Addr().String())
			go func() {
				defer close(serveDone)
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("HTTP server failed", "error", err)
					if err := shutdown.Shutdown(fx.ExitCode(1)); err != nil {
						logger.Error("request shutdown failed", "error", err)
					}
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				// Force release if active requests exceed the graceful shutdown budget.
				return errors.Join(err, server.Close())
			}
			select {
			case <-serveDone:
				logger.Info("HTTP server stopped")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
