package bootstrap

import (
	"fmt"
	oidcauth "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/oidc"
	"log/slog"
	"net"
	"os"
	"strconv"
)

// Config contains only settings used by the current application.
// DatabaseURL is optional while no business endpoint exists: when set, the
// database module starts and pgxpool.ParseConfig validates the value before
// the HTTP listener opens; when empty, the application runs without a pool.
type Config struct {
	HTTPAddr     string
	LogLevel     slog.Level
	DatabaseURL  string
	OIDCIssuer   string
	OIDCAudience string
}

func LoadConfig() (Config, error) {
	cfg := Config{HTTPAddr: ":8080", LogLevel: slog.LevelInfo}
	if value, exists := os.LookupEnv("HTTP_ADDR"); exists {
		cfg.HTTPAddr = value
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must have the form host:port: %w", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR port must be an integer between 1 and 65535")
	}
	if value, exists := os.LookupEnv("LOG_LEVEL"); exists {
		switch value {
		case "debug":
			cfg.LogLevel = slog.LevelDebug
		case "info":
			cfg.LogLevel = slog.LevelInfo
		case "warn":
			cfg.LogLevel = slog.LevelWarn
		case "error":
			cfg.LogLevel = slog.LevelError
		default:
			return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
		}
	}
	if value, exists := os.LookupEnv("DATABASE_URL"); exists {
		cfg.DatabaseURL = value
	}
	cfg.OIDCIssuer = os.Getenv("OIDC_ISSUER")
	cfg.OIDCAudience = os.Getenv("OIDC_AUDIENCE")
	if cfg.OIDCIssuer != "" || cfg.OIDCAudience != "" {
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required for authenticated business endpoints")
		}
		if err := (oidcauth.Config{Issuer: cfg.OIDCIssuer, Audience: cfg.OIDCAudience}).Validate(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}
