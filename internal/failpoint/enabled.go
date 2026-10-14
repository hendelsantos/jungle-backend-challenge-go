//go:build faulttest

package failpoint

import (
	"context"
	"log/slog"
)

const Enabled = true

// Hold has no timer: only SIGKILL or lifecycle cancellation releases the test
// process. The caller returns without acknowledging the durable operation.
func Hold(ctx context.Context, logger *slog.Logger, point string, fields ...any) {
	if logger != nil {
		logger.Info("test failpoint reached", append([]any{"failpoint", point}, fields...)...)
	}
	<-ctx.Done()
}
