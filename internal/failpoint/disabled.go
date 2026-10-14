//go:build !faulttest

// Package failpoint provides crash synchronization only in dedicated test builds.
package failpoint

import (
	"context"
	"log/slog"
)

const Enabled = false

func Hold(context.Context, *slog.Logger, string, ...any) {}
