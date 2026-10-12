package command

import (
	"testing"
	"time"
)

func TestOutboxBackoffCapsBeforeShift(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second}, {1, time.Second}, {2, 2 * time.Second}, {9, 256 * time.Second}, {10, 5 * time.Minute},
		{1000, 5 * time.Minute}, {int(^uint(0) >> 1), 5 * time.Minute},
	} {
		if got := outboxBackoff(tc.attempt); got != tc.want {
			t.Errorf("attempt=%d got=%v want=%v", tc.attempt, got, tc.want)
		}
	}
}
