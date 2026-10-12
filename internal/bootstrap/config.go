package bootstrap

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
)

// Config contains only settings used by the current application.
type Config struct {
	HTTPAddr string
	LogLevel slog.Level
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
	return cfg, nil
}
