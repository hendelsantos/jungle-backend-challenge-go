package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ReadinessCheck names one dependency probed by GET /health/ready.
// SQS joins this route when the messaging adapter exists.
type ReadinessCheck struct {
	Name  string
	Check func(context.Context) error
}

// RegisterReadiness exposes dependency readiness on the public route. The
// response carries status only: dependency failure details are not leaked.
func RegisterReadiness(mux *http.ServeMux, checks ...ReadinessCheck) {
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		results := make(map[string]string, len(checks))
		ready := true
		for _, check := range checks {
			if check.Check == nil || check.Check(ctx) != nil {
				results[check.Name] = "unavailable"
				ready = false
				continue
			}
			results[check.Name] = "ok"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		status, value := http.StatusOK, "ready"
		if !ready {
			status, value = http.StatusServiceUnavailable, "unavailable"
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Status string            `json:"status"`
			Checks map[string]string `json:"checks"`
		}{value, results})
	})
}
