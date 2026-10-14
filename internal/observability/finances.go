package observability

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// FinancialMetrics carries the financial operation metrics of this process.
// Labels stay bounded: kind is the six wager kinds and status the five
// terminal/terminal states; player, wallet and provider identity are never
// labels. Like OutboxMetrics it is a registered singleton.
type FinancialMetrics struct {
	Operations      *prometheus.CounterVec   // {kind, status}
	Replays         prometheus.Counter       // idempotent replays served from storage
	Conflicts       prometheus.Counter       // 409 identity conflicts
	Duration        *prometheus.HistogramVec // {kind} processing seconds
	DbRetries       *prometheus.CounterVec   // {reason} transaction retries
	Redeliveries    prometheus.Counter       // SQS messages consumed again
	DlqFailures     prometheus.Counter
	Dlq             prometheus.Counter // messages copied to the DLQ
	Reconciliations prometheus.Counter // reconciliation runs
	Divergences     prometheus.Counter // reconciliation runs with difference
}

var (
	financeMetricsOnce sync.Once
	financeMetrics     *FinancialMetrics
)

// NewFinancialMetrics returns the shared financial counters.
func NewFinancialMetrics() *FinancialMetrics {
	financeMetricsOnce.Do(func() {
		counter := func(name, help string) prometheus.Counter {
			return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
		}
		m := &FinancialMetrics{
			Operations: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "jungle_wager_operations_total", Help: "Wager operations concluded per kind and persisted status.",
			}, []string{"kind", "status"}),
			Replays:   counter("jungle_wager_idempotent_replays_total", "Operations answered from stored identity without effects."),
			Conflicts: counter("jungle_wager_idempotency_conflicts_total", "Identity reuses refused with 409."),
			Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name: "jungle_wager_processing_duration_seconds", Help: "Use-case processing duration per kind.",
			}, []string{"kind"}),
			DbRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "jungle_wager_db_retries_total", Help: "PostgreSQL transaction retries per reason.",
			}, []string{"reason"}),
			Redeliveries:    counter("jungle_wager_sqs_redeliveries_total", "SQS messages consumed with a receive count above one."),
			DlqFailures:     counter("jungle_wager_dlq_publish_failures_total", "Failed explicit DLQ copy attempts; source remains unacknowledged."),
			Dlq:             counter("jungle_wager_dlq_total", "Messages copied to the dead-letter queue."),
			Reconciliations: counter("jungle_wallet_reconciliations_total", "Wallet reconciliation runs."),
			Divergences:     counter("jungle_wallet_reconciliation_divergences_total", "Reconciliation runs where ledger sum differs from balance."),
		}
		Prometheus.MustRegister(m.Operations, m.Replays, m.Conflicts, m.Duration, m.DbRetries,
			m.Redeliveries, m.Dlq, m.DlqFailures, m.Reconciliations, m.Divergences)
		financeMetrics = m
	})
	return financeMetrics
}

// MetricKind bounds labels even for untrusted contract values.
func MetricKind(kind string) string {
	switch kind {
	case "BET", "WIN", "LOSS", "REFUND", "ROLLBACK", "OPENING":
		return kind
	}
	return "INVALID"
}
