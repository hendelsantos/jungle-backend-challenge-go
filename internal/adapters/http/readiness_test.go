package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func readinessMux(checks ...ReadinessCheck) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterReadiness(mux, checks...)
	return mux
}

func TestReadinessRoute(t *testing.T) {
	ok := func(context.Context) error { return nil }
	failing := func(context.Context) error { return errors.New("dependency down") }
	for _, tc := range []struct {
		name   string
		mux    *http.ServeMux
		method string
		status int
		body   string
	}{
		{
			name:   "all checks pass",
			mux:    readinessMux(ReadinessCheck{Name: "postgres", Check: ok}),
			method: http.MethodGet,
			status: http.StatusOK,
			body:   `{"status":"ready","checks":{"postgres":"ok"}}`,
		},
		{
			name:   "one check fails",
			mux:    readinessMux(ReadinessCheck{Name: "postgres", Check: ok}, ReadinessCheck{Name: "sqs", Check: failing}),
			method: http.MethodGet,
			status: http.StatusServiceUnavailable,
			body:   `{"status":"unavailable","checks":{"postgres":"ok","sqs":"unavailable"}}`,
		},
		{
			name:   "nil check is unavailable",
			mux:    readinessMux(ReadinessCheck{Name: "postgres"}),
			method: http.MethodGet,
			status: http.StatusServiceUnavailable,
			body:   `{"status":"unavailable","checks":{"postgres":"unavailable"}}`,
		},
		{
			name:   "method not allowed",
			mux:    readinessMux(ReadinessCheck{Name: "postgres", Check: ok}),
			method: http.MethodPost,
			status: http.StatusMethodNotAllowed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			tc.mux.ServeHTTP(response, httptest.NewRequest(tc.method, "/health/ready", nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.status == http.StatusOK || tc.status == http.StatusServiceUnavailable {
				if got := response.Body.String(); got != tc.body+"\n" {
					t.Fatalf("body = %q, want %q", got, tc.body+"\n")
				}
				if response.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
				}
			}
		})
	}
}
