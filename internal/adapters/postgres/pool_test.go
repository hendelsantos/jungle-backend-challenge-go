package postgres

import (
	"testing"
	"time"
)

func TestNewPoolRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"empty url", ""},
		{"malformed url", "%zz://not a url"},
		{"unsupported scheme", "mysql://user@localhost/db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPool(Config{URL: tc.url}); err == nil {
				t.Fatalf("NewPool(%q) accepted an invalid configuration", tc.url)
			}
		})
	}
}

// pgxpool does not connect during construction, so a syntactically valid URL
// creates a pool even without a live server; readiness belongs to Ping.
func TestNewPoolCreatesLazilyWithDefaults(t *testing.T) {
	pool, err := NewPool(Config{URL: "postgres://user@localhost:5432/jungle?sslmode=disable"})
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	t.Cleanup(pool.Close)
	config := pool.Config()
	if config.MaxConnLifetime != 30*time.Minute {
		t.Fatalf("MaxConnLifetime: got %v, want 30m", config.MaxConnLifetime)
	}
	if config.MaxConnIdleTime != 5*time.Minute {
		t.Fatalf("MaxConnIdleTime: got %v, want 5m", config.MaxConnIdleTime)
	}
	if config.HealthCheckPeriod != time.Minute {
		t.Fatalf("HealthCheckPeriod: got %v, want 1m", config.HealthCheckPeriod)
	}
}
