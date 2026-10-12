package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthRoutes(t *testing.T) {
	server := NewServer(":8080")
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/health/live", http.StatusOK},
		{http.MethodPost, "/health/live", http.StatusMethodNotAllowed},
		{http.MethodGet, "/health/ready", http.StatusNotFound},
		{http.MethodGet, "/wallets", http.StatusNotFound},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				if response.Header().Get("Content-Type") != "application/json" || response.Body.String() != "{\"status\":\"ok\"}\n" {
					t.Fatalf("unexpected health response: %v %s", response.Header(), response.Body.String())
				}
			}
		})
	}
}
