package bootstrap

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	for _, key := range []string{"HTTP_ADDR", "LOG_LEVEL", "DATABASE_URL"} {
		t.Setenv(key, "") // Restore the caller's environment when the test finishes.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadConfig()
	if err != nil || cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.DatabaseURL != "" {
		t.Fatalf("unexpected defaults: %+v, %v", cfg, err)
	}
}

func TestLoadConfigDatabaseURL(t *testing.T) {
	t.Run("unset leaves database empty", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		if err := os.Unsetenv("DATABASE_URL"); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig()
		if err != nil || cfg.DatabaseURL != "" {
			t.Fatalf("unexpected database url: %+v, %v", cfg, err)
		}
	})
	t.Run("set value is preserved verbatim", func(t *testing.T) {
		// Validation of the connection string belongs to pgxpool.ParseConfig.
		url := "postgres://user:secret@localhost:5432/jungle?sslmode=disable"
		t.Setenv("DATABASE_URL", url)
		cfg, err := LoadConfig()
		if err != nil || cfg.DatabaseURL != url {
			t.Fatalf("database url was not preserved: %+v, %v", cfg, err)
		}
	})
}

func TestLoadConfigReferenceWorker(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("REFERENCE_TTL", "")
		if err := os.Unsetenv("REFERENCE_TTL"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("REFERENCE_WORKER_INTERVAL", "")
		if err := os.Unsetenv("REFERENCE_WORKER_INTERVAL"); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReferenceTTL != 24*time.Hour || cfg.ReferenceWorkerInterval != time.Second {
			t.Fatalf("unexpected reference defaults: %+v", cfg)
		}
	})
	t.Run("valid values are preserved", func(t *testing.T) {
		t.Setenv("REFERENCE_TTL", "250ms")
		t.Setenv("REFERENCE_WORKER_INTERVAL", "2s")
		cfg, err := LoadConfig()
		if err != nil || cfg.ReferenceTTL != 250*time.Millisecond || cfg.ReferenceWorkerInterval != 2*time.Second {
			t.Fatalf("reference settings were not preserved: %+v, %v", cfg, err)
		}
	})
	t.Run("invalid values are rejected", func(t *testing.T) {
		for _, tc := range []struct{ name, ttl, interval string }{
			{"bad ttl", "24hours", "1s"},
			{"zero ttl", "0", "1s"},
			{"negative ttl", "-1s", "1s"},
			{"bad interval", "1s", "fast"},
			{"zero interval", "1s", "0"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("REFERENCE_TTL", tc.ttl)
				t.Setenv("REFERENCE_WORKER_INTERVAL", tc.interval)
				if _, err := LoadConfig(); err == nil {
					t.Fatalf("expected rejection for ttl=%q interval=%q", tc.ttl, tc.interval)
				}
			})
		}
	})
}

func TestLoadConfig(t *testing.T) {
	for _, tc := range []struct {
		name, address, level string
		wantError            bool
	}{
		{"valid", "127.0.0.1:8081", "debug", false},
		{"ipv6", "[::1]:8080", "warn", false},
		{"all interfaces", ":8080", "error", false},
		{"empty address", "", "info", true},
		{"missing port", "localhost", "info", true},
		{"named port", ":http", "info", true},
		{"zero port", ":0", "info", true},
		{"negative port", ":-1", "info", true},
		{"overflow port", ":65536", "info", true},
		{"invalid level", ":8080", "verbose", true},
		{"empty level", ":8080", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", tc.address)
			t.Setenv("LOG_LEVEL", tc.level)
			cfg, err := LoadConfig()
			if (err != nil) != tc.wantError {
				t.Fatalf("config = %+v, error = %v, wantError = %v", cfg, err, tc.wantError)
			}
			if !tc.wantError {
				var level slog.Level
				if err := level.UnmarshalText([]byte(tc.level)); err != nil {
					t.Fatal(err)
				}
				if cfg.HTTPAddr != tc.address || cfg.LogLevel != level {
					t.Fatalf("configuration was not preserved: %+v", cfg)
				}
			}
		})
	}
}

func TestSQSConfigRequiresProviderTrustAndDatabase(t *testing.T) {
	t.Setenv("SQS_ENDPOINT_URL", "http://localhost:4566")
	t.Setenv("DATABASE_URL", "postgres://local")
	t.Setenv("SQS_PROVIDER_KEYS", `{"provider-a":"local-test-provider-a-key-32-bytes-minimum"}`)
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", `{}`, `{"provider-a":"short"}`, `null`, `{"":"local-test-provider-a-key-32-bytes-minimum"}`} {
		t.Run(invalid, func(t *testing.T) {
			t.Setenv("SQS_PROVIDER_KEYS", invalid)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("missing trust accepted")
			}
		})
	}
	t.Setenv("DATABASE_URL", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("SQS enabled without transactional database")
	}
}

func TestSQSEnabledGate(t *testing.T) {
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("DATABASE_URL", "postgres://local")
		t.Setenv("SQS_PROVIDER_KEYS", `{"provider-a":"local-test-provider-a-key-32-bytes-minimum"}`)
	}
	t.Run("auto keeps endpoint behavior", func(t *testing.T) {
		base(t)
		t.Setenv("SQS_ENDPOINT_URL", "http://localhost:4566")
		for _, value := range []string{"", "auto"} {
			t.Setenv("SQS_ENABLED", value)
			cfg, err := LoadConfig()
			if err != nil || !cfg.SQSEnabled {
				t.Fatalf("SQS_ENABLED=%q must enable messaging with endpoint: %+v, %v", value, cfg, err)
			}
		}
	})
	t.Run("unset without endpoint disables", func(t *testing.T) {
		base(t)
		cfg, err := LoadConfig()
		if err != nil || cfg.SQSEnabled {
			t.Fatalf("empty endpoint must disable messaging: %+v, %v", cfg, err)
		}
	})
	t.Run("true forces on with the managed service", func(t *testing.T) {
		base(t)
		t.Setenv("SQS_ENABLED", "true")
		cfg, err := LoadConfig()
		if err != nil || !cfg.SQSEnabled || cfg.SQSEndpointURL != "" || cfg.SQSRegion != "us-east-1" {
			t.Fatalf("managed homologation configuration rejected: %+v, %v", cfg, err)
		}
		t.Setenv("SQS_REGION", "sa-east-1")
		cfg, err = LoadConfig()
		if err != nil || cfg.SQSRegion != "sa-east-1" {
			t.Fatalf("region override rejected: %+v, %v", cfg, err)
		}
	})
	t.Run("true still requires database and trust", func(t *testing.T) {
		base(t)
		t.Setenv("SQS_ENABLED", "1")
		t.Setenv("DATABASE_URL", "")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("SQS_ENABLED=true accepted without database")
		}
		t.Setenv("DATABASE_URL", "postgres://local")
		t.Setenv("SQS_PROVIDER_KEYS", "")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("SQS_ENABLED=true accepted without provider keys")
		}
	})
	t.Run("false forces off even with endpoint", func(t *testing.T) {
		base(t)
		t.Setenv("SQS_ENDPOINT_URL", "http://localhost:4566")
		for _, value := range []string{"false", "0"} {
			t.Setenv("SQS_ENABLED", value)
			cfg, err := LoadConfig()
			if err != nil || cfg.SQSEnabled {
				t.Fatalf("SQS_ENABLED=%q must disable messaging: %+v, %v", value, cfg, err)
			}
		}
		t.Setenv("SQS_ENABLED", "false")
		t.Setenv("DATABASE_URL", "")
		if _, err := LoadConfig(); err != nil {
			t.Fatalf("disabled SQS must not require a database: %v", err)
		}
	})
	t.Run("invalid value rejected", func(t *testing.T) {
		base(t)
		t.Setenv("SQS_ENABLED", "maybe")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("invalid SQS_ENABLED accepted")
		}
	})
}
