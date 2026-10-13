package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
)

// outboxStatsCollector exposes the publisher backlog as Prometheus gauges.
// The values are computed from PostgreSQL for every scrape, so a failed query
// becomes an invalid metric on the output instead of a stale zero.
type outboxStatsCollector struct {
	store     ports.OutboxDeliveryRepository
	pending   *prometheus.Desc
	oldestAge *prometheus.Desc
}

// PoolGauges carries a per-process snapshot of the PostgreSQL connection pool
// statistics; bootstrap bridges the concrete pgxpool type into this plain
// struct, so the HTTP adapter never imports pgx.
type PoolGauges struct {
	Total             int
	Idle              int
	Acquired          int
	AcquireDurationMs float64
}

// PoolStatsProvider returns the pool snapshot at scrape time.
type PoolStatsProvider func() PoolGauges

// poolCollector renders pgxpool gauges with zero-cardinality labels.
type poolCollector struct {
	provider PoolStatsProvider
	total    *prometheus.Desc
	idle     *prometheus.Desc
	acquired *prometheus.Desc
	acqMs    *prometheus.Desc
}

var outboxGaugesOnce sync.Once

// RegisterMetrics exposes the shared Prometheus registry at GET /metrics.
// No financial data or high-cardinality labels appear on the output.
func RegisterMetrics(mux *http.ServeMux, store ports.OutboxDeliveryRepository, pool PoolStatsProvider) {
	outboxGaugesOnce.Do(func() {
		observability.Prometheus.MustRegister(outboxStatsCollector{
			store: store,
			pending: prometheus.NewDesc("jungle_outbox_pending",
				"Committed outbox events not yet published.", nil, nil),
			oldestAge: prometheus.NewDesc("jungle_outbox_oldest_age_seconds",
				"Age of the oldest unpublished outbox event.", nil, nil),
		})
	})
	// The pool collector is instance-specific; every replica registers its own.
	observability.Prometheus.MustRegister(poolCollector{
		provider: pool,
		total: prometheus.NewDesc("jungle_pgpool_total_conns",
			"PostgreSQL connections pooled by this process.", nil, nil),
		idle: prometheus.NewDesc("jungle_pgpool_idle_conns",
			"Idle pooled connections right now.", nil, nil),
		acquired: prometheus.NewDesc("jungle_pgpool_acquired_conns",
			"Currently acquired pooled connections.", nil, nil),
		acqMs: prometheus.NewDesc("jungle_pgpool_acquire_duration_ms",
			"Mean wait for an acquired connection.", nil, nil),
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(observability.Prometheus, promhttp.HandlerOpts{}))
}

func (c outboxStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func (c outboxStatsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stats, err := c.store.Stats(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.pending, err)
		ch <- prometheus.NewInvalidMetric(c.oldestAge, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(stats.Pending))
	ch <- prometheus.MustNewConstMetric(c.oldestAge, prometheus.GaugeValue, float64(stats.OldestAgeSeconds))
}

func (c poolCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func (c poolCollector) Collect(ch chan<- prometheus.Metric) {
	gauges := c.provider()
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, float64(gauges.Total))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(gauges.Idle))
	ch <- prometheus.MustNewConstMetric(c.acquired, prometheus.GaugeValue, float64(gauges.Acquired))
	ch <- prometheus.MustNewConstMetric(c.acqMs, prometheus.GaugeValue, gauges.AcquireDurationMs)
}
