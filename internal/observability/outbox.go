// Package observability holds process-local counters without framework dependencies.
package observability

import "sync/atomic"

// These counters describe attempts on this process. Backlog gauges are queried
// from PostgreSQL by the metrics handler, not cached independently by replicas.
type OutboxMetrics struct {
	Claimed              atomic.Uint64
	Published            atomic.Uint64
	PublishFailures      atomic.Uint64
	ConfirmationFailures atomic.Uint64
	Retries              atomic.Uint64
	LeaseLost            atomic.Uint64
}
