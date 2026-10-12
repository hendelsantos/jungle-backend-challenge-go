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

var outboxGaugesOnce sync.Once

// RegisterMetrics exposes the shared Prometheus registry at GET /metrics.
// No financial data or high-cardinality labels appear on the output.
func RegisterMetrics(mux *http.ServeMux, store ports.OutboxDeliveryRepository) {
	outboxGaugesOnce.Do(func() {
		observability.Prometheus.MustRegister(outboxStatsCollector{
			store: store,
			pending: prometheus.NewDesc("jungle_outbox_pending",
				"Committed outbox events not yet published.", nil, nil),
			oldestAge: prometheus.NewDesc("jungle_outbox_oldest_age_seconds",
				"Age of the oldest unpublished outbox event.", nil, nil),
		})
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
