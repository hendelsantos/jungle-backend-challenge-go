// Package observability holds the shared Prometheus registry and the
// process-local metric sets. Nothing here reaches the database during a
// scrape except the outbox gauges registered by the metrics handler.
package observability

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Prometheus is the registry served at GET /metrics. A dedicated registry
// keeps Go runtime metrics out of the scraper contract.
var Prometheus = prometheus.NewRegistry()

// OutboxMetrics counts publishing attempts of this process. All counters are
// Prometheus backed and registered once; a package singleton keeps repeated
// constructors (bootstrap, tests) from double-registering.
type OutboxMetrics struct {
	Claimed              prometheus.Counter
	Published            prometheus.Counter
	PublishFailures      prometheus.Counter
	ConfirmationFailures prometheus.Counter
	Retries              prometheus.Counter
	LeaseLost            prometheus.Counter
}

var (
	outboxMetricsOnce sync.Once
	outboxMetrics     *OutboxMetrics
)

// NewOutboxMetrics returns the shared outbox counters.
func NewOutboxMetrics() *OutboxMetrics {
	outboxMetricsOnce.Do(func() {
		counts := func(name, help string) prometheus.Counter {
			return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
		}
		outboxMetrics = &OutboxMetrics{
			Claimed:              counts("jungle_outbox_claimed_total", "Outbox events claimed for publishing."),
			Published:            counts("jungle_outbox_published_total", "Outbox events confirmed published."),
			PublishFailures:      counts("jungle_outbox_publish_failures_total", "Outbox publish attempts that failed."),
			ConfirmationFailures: counts("jungle_outbox_confirmation_failures_total", "Outbox acknowledgements that failed after a send."),
			Retries:              counts("jungle_outbox_retries_total", "Outbox events claimed again after a failed attempt."),
			LeaseLost:            counts("jungle_outbox_lease_lost_total", "Confirmations and reschedules rejected by an expired lease."),
		}
		Prometheus.MustRegister(outboxMetrics.Claimed, outboxMetrics.Published, outboxMetrics.PublishFailures,
			outboxMetrics.ConfirmationFailures, outboxMetrics.Retries, outboxMetrics.LeaseLost)
	})
	return outboxMetrics
}
