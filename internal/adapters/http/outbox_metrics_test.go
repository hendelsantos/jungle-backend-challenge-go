package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/http"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/ports"
)

type auditStore struct{ pending int64 }

func (auditStore) ClaimNext(context.Context, string, time.Duration) (ports.OutboxDelivery, bool, error) {
	return ports.OutboxDelivery{}, false, nil
}
func (auditStore) MarkPublished(context.Context, uuid.UUID, string) error { return nil }
func (auditStore) ScheduleRetry(context.Context, uuid.UUID, string, time.Duration, string) error {
	return nil
}
func (s auditStore) Stats(context.Context) (ports.OutboxStats, error) {
	return ports.OutboxStats{Pending: s.pending, OldestAgeSeconds: 9}, nil
}

func TestRegisterMetricsIsIdempotentAndRetargetsPool(t *testing.T) {
	// Separate mounts (as repeated Fx apps do): the shared registry must not
	// re-register metric names, and the provider must retarget.
	var first httpapi.PoolStatsProvider = func() httpapi.PoolGauges { return httpapi.PoolGauges{Total: 1} }
	httpapi.RegisterMetrics(&http.ServeMux{}, auditStore{pending: 1}, first)

	second := uint32(0)
	var latest httpapi.PoolStatsProvider = func() httpapi.PoolGauges { atomic.StoreUint32(&second, 2); return httpapi.PoolGauges{Total: 7} }
	mux := &http.ServeMux{}
	httpapi.RegisterMetrics(mux, auditStore{pending: 3}, latest)

	server := httptest.NewServer(mux)
	defer server.Close()
	body, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Body.Close()
	data, _ := io.ReadAll(body.Body)
	if !strings.Contains(string(data), "jungle_pgpool_total_conns 7") {
		t.Fatalf("pool gauge did not retarget to the latest mount: %s", data)
	}
	if !strings.Contains(string(data), "jungle_outbox_pending 3") {
		t.Fatalf("outbox gauge missing: %s", data)
	}
	if atomic.LoadUint32(&second) != 2 {
		t.Fatal("pool provider of the latest mount was not consulted")
	}
}

func TestMetricsMountsKeepTheirOwnDependencies(t *testing.T) {
	muxes := []*http.ServeMux{{}, {}}
	for i, mux := range muxes {
		total := i + 1
		httpapi.RegisterMetrics(mux, auditStore{pending: int64(total)}, func() httpapi.PoolGauges { return httpapi.PoolGauges{Total: total} })
	}
	for i, mux := range muxes {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), fmt.Sprintf("jungle_outbox_pending %d", i+1)) || !strings.Contains(response.Body.String(), fmt.Sprintf("jungle_pgpool_total_conns %d", i+1)) {
			t.Fatalf("mount %d used another application's dependencies: %s", i, response.Body.String())
		}
	}
}
