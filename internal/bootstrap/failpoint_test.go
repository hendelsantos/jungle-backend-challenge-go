//go:build !faulttest

package bootstrap

import (
	"strings"
	"testing"
)

func TestNormalBuildRejectsCrashInstrumentation(t *testing.T) {
	for _, key := range []string{"JUNGLE_TEST_POST_COMMIT_PAUSE", "JUNGLE_TEST_PUBLISH_ACK_PAUSE"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "1s")
			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), "faulttest build") {
				t.Fatalf("normal build accepted crash instrumentation: %v", err)
			}
		})
	}
}
