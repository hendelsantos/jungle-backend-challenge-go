package observability

import "testing"

func TestMetricKindBoundsUntrustedLabels(t *testing.T) {
	for _, k := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK", "OPENING"} {
		if MetricKind(k) != k {
			t.Fatal(k)
		}
	}
	for _, k := range []string{"", "attacker-controlled", "bet"} {
		if MetricKind(k) != "INVALID" {
			t.Fatal(k)
		}
	}
}
