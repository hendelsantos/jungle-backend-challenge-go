package bootstrap

import (
	"log/slog"
	"os"
	"testing"
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
