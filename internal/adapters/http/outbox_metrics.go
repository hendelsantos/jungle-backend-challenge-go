package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/observability"
)

// RegisterOutboxMetrics exposes Prometheus text with no financial data or
// high-cardinality labels. A failed DB query yields 503, never a false zero lag.
func RegisterOutboxMetrics(mux *http.ServeMux, store ports.OutboxDeliveryRepository, m *observability.OutboxMetrics) {
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		stats, err := store.Stats(ctx)
		if err != nil {
			http.Error(w, "outbox metrics unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		for _, metric := range []struct {
			name, kind string
			value      uint64
		}{
			{"jungle_outbox_pending", "gauge", uint64(stats.Pending)},
			{"jungle_outbox_oldest_age_seconds", "gauge", uint64(stats.OldestAgeSeconds)},
			{"jungle_outbox_claimed_total", "counter", m.Claimed.Load()},
			{"jungle_outbox_published_total", "counter", m.Published.Load()},
			{"jungle_outbox_publish_failures_total", "counter", m.PublishFailures.Load()},
			{"jungle_outbox_confirmation_failures_total", "counter", m.ConfirmationFailures.Load()},
			{"jungle_outbox_retries_total", "counter", m.Retries.Load()},
			{"jungle_outbox_lease_lost_total", "counter", m.LeaseLost.Load()},
		} {
			fmt.Fprintf(w, "# TYPE %s %s\n%s %d\n", metric.name, metric.kind, metric.name, metric.value)
		}
	})
}
