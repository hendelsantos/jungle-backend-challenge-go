//go:build faulttest

package failpoint

import (
	"context"
	"testing"
	"time"
)

func TestHoldWaitsForLifecycleCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { Hold(ctx, nil, "test"); close(done) }()
	select {
	case <-done:
		t.Fatal("failpoint released without cancellation")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failpoint ignored cancellation")
	}
}
