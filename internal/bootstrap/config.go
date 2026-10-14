package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	oidcauth "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/oidc"
	sqsadapter "github.com/hendelsantos/jungle-backend-challenge-go/internal/adapters/sqs"
	"github.com/hendelsantos/jungle-backend-challenge-go/internal/application/command"
)

// Config contains only settings used by the current application.
// DatabaseURL is optional while no business endpoint exists: when set, the
// database module starts and pgxpool.ParseConfig validates the value before
// the HTTP listener opens; when empty, the application runs without a pool.
type Config struct {
	HTTPAddr                string
	LogLevel                slog.Level
	DatabaseURL             string
	OutboxWorkers           int
	TestPostCommitPause     time.Duration
	TestPublishAckPause     time.Duration
	PGPoolMaxConns          int
	OIDCIssuer              string
	OIDCAudience            string
	ReferenceTTL            time.Duration
	ReferenceWorkerInterval time.Duration
	SQSQueuePrefix          string
	SQSEndpointURL          string
	SQSRegion               string
	SQSProviderKeys         sqsadapter.ProviderKeys
}

func LoadConfig() (Config, error) {
	cfg := Config{HTTPAddr: ":8080", LogLevel: slog.LevelInfo,
		ReferenceTTL: command.DefaultReferenceTTL, ReferenceWorkerInterval: time.Second}
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
	if value, exists := os.LookupEnv("REFERENCE_TTL"); exists {
		ttl, err := time.ParseDuration(value)
		if err != nil || ttl <= 0 {
			return Config{}, fmt.Errorf("REFERENCE_TTL must be a positive duration like 24h or 250ms")
		}
		cfg.ReferenceTTL = ttl
	}
	if value, exists := os.LookupEnv("REFERENCE_WORKER_INTERVAL"); exists {
		interval, err := time.ParseDuration(value)
		if err != nil || interval <= 0 {
			return Config{}, fmt.Errorf("REFERENCE_WORKER_INTERVAL must be a positive duration like 1s or 250ms")
		}
		cfg.ReferenceWorkerInterval = interval
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
	if text := os.Getenv("JUNGLE_TEST_POST_COMMIT_PAUSE"); text != "" {
		d, err := time.ParseDuration(text)
		if err != nil {
			return Config{}, fmt.Errorf("JUNGLE_TEST_POST_COMMIT_PAUSE must be a duration up to 1m: %w", err)
		}
		if d < 0 || d > time.Minute {
			return Config{}, fmt.Errorf("JUNGLE_TEST_POST_COMMIT_PAUSE must be a duration up to 1m, got %s", d)
		}
		cfg.TestPostCommitPause = d
	}
	if text := os.Getenv("JUNGLE_TEST_PUBLISH_ACK_PAUSE"); text != "" {
		d, err := time.ParseDuration(text)
		if err != nil {
			return Config{}, fmt.Errorf("JUNGLE_TEST_PUBLISH_ACK_PAUSE must be a duration up to 1m: %w", err)
		}
		if d < 0 || d > time.Minute {
			return Config{}, fmt.Errorf("JUNGLE_TEST_PUBLISH_ACK_PAUSE must be a duration up to 1m, got %s", d)
		}
		cfg.TestPublishAckPause = d
	}
	if text := os.Getenv("OUTBOX_WORKERS"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > 16 {
			return Config{}, fmt.Errorf("OUTBOX_WORKERS must be an integer between 1 and 16: %w", err)
		}
		cfg.OutboxWorkers = n
	}
	if text := os.Getenv("PG_POOL_MAX_CONNS"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > 100 {
			return Config{}, fmt.Errorf("PG_POOL_MAX_CONNS must be an integer between 1 and 100: %w", err)
		}
		cfg.PGPoolMaxConns = n
	}
	cfg.SQSQueuePrefix = os.Getenv("SQS_QUEUE_PREFIX")
	cfg.SQSEndpointURL = os.Getenv("SQS_ENDPOINT_URL")
	cfg.SQSRegion = os.Getenv("SQS_REGION")
	if cfg.SQSRegion == "" {
		cfg.SQSRegion = "us-east-1"
	}
	// The consumer is transactional: it only composes with the database.
	if cfg.SQSEndpointURL != "" && cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required for the SQS consumer")
	}
	if cfg.SQSEndpointURL != "" {
		if err := (sqsadapter.Config{EndpointURL: cfg.SQSEndpointURL, Region: cfg.SQSRegion, QueuePrefix: cfg.SQSQueuePrefix}).Validate(); err != nil {
			return Config{}, err
		}
		if err := json.Unmarshal([]byte(os.Getenv("SQS_PROVIDER_KEYS")), &cfg.SQSProviderKeys); err != nil {
			return Config{}, fmt.Errorf("SQS_PROVIDER_KEYS must be a JSON object of provider signing keys")
		}
		if err := cfg.SQSProviderKeys.Validate(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}
