package config

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/joho/godotenv"
	"go-simpler.org/env"
)

type Config struct {
	AppEnv             string `env:"APP_ENV" default:"development"`
	Port               string `env:"PORT" default:"8080"`
	DatabaseURL        string `env:"DATABASE_URL"`
	TwitchClientID     string `env:"TWITCH_CLIENT_ID"`
	TwitchClientSecret string `env:"TWITCH_CLIENT_SECRET"`
	TwitchRedirectURI  string `env:"TWITCH_REDIRECT_URI"`
	SessionSecret      string `env:"SESSION_SECRET"`
	TokenEncryptionKey string `env:"TOKEN_ENCRYPTION_KEY"`
	WebhookCallbackURL string `env:"WEBHOOK_CALLBACK_URL"`
	WebhookSecret      string `env:"WEBHOOK_SECRET"`
	BotUserID          string `env:"BOT_USER_ID"`
	RedisURL           string `env:"REDIS_URL"`
	LogLevel           string `env:"LOG_LEVEL" default:"info"`
	LogFormat          string `env:"LOG_FORMAT" default:"text"`

	// WebSocket connection limits
	MaxWebSocketConnections int     `env:"MAX_WEBSOCKET_CONNECTIONS" default:"10000"`
	MaxConnectionsPerIP     int     `env:"MAX_CONNECTIONS_PER_IP" default:"100"`
	ConnectionRatePerIP     float64 `env:"CONNECTION_RATE_PER_IP" default:"10"`
	ConnectionRateBurst     int     `env:"CONNECTION_RATE_BURST" default:"20"`

	// Vote rate limiting (token bucket)
	VoteRateLimitCapacity int `env:"VOTE_RATE_LIMIT_CAPACITY" default:"100"`
	VoteRateLimitRate     int `env:"VOTE_RATE_LIMIT_RATE" default:"100"`

	// PostgreSQL connection pool configuration
	DBMinConns          int32         `env:"DB_MIN_CONNS" default:"2"`
	DBMaxConns          int32         `env:"DB_MAX_CONNS" default:"10"`
	DBMaxConnIdleTime   time.Duration `env:"DB_MAX_CONN_IDLE_TIME" default:"5m"`
	DBHealthCheckPeriod time.Duration `env:"DB_HEALTH_CHECK_PERIOD" default:"1m"`
	DBConnectTimeout    time.Duration `env:"DB_CONNECT_TIMEOUT" default:"5s"`
	DBMaxRetries        int           `env:"DB_MAX_RETRIES" default:"3"`
	DBInitialBackoff    time.Duration `env:"DB_INITIAL_BACKOFF" default:"1s"`

	// Performance tuning (optional, defaults optimized for production)
	BroadcasterMaxClientsPerSession int           `env:"BROADCASTER_MAX_CLIENTS_PER_SESSION" default:"50"`
	BroadcasterTickInterval         time.Duration `env:"BROADCASTER_TICK_INTERVAL" default:"50ms"`
	CleanupInterval                 time.Duration `env:"CLEANUP_INTERVAL" default:"30s"`
	OrphanMaxAge                    time.Duration `env:"ORPHAN_MAX_AGE" default:"30s"`
	SessionMaxAge                   time.Duration `env:"SESSION_MAX_AGE" default:"168h"` // 7 days
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		slog.Info("No .env file found, using environment variables")
	}

	var cfg Config
	if err := env.Load(&cfg, nil); err != nil {
		return nil, err
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	required := map[string]string{
		"DATABASE_URL":         cfg.DatabaseURL,
		"TWITCH_CLIENT_ID":     cfg.TwitchClientID,
		"TWITCH_CLIENT_SECRET": cfg.TwitchClientSecret,
		"TWITCH_REDIRECT_URI":  cfg.TwitchRedirectURI,
		"SESSION_SECRET":       cfg.SessionSecret,
		"REDIS_URL":            cfg.RedisURL,
	}
	for name, value := range required {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	if cfg.WebhookCallbackURL != "" || cfg.WebhookSecret != "" {
		if cfg.WebhookCallbackURL == "" {
			return fmt.Errorf("WEBHOOK_CALLBACK_URL is required when WEBHOOK_SECRET is set")
		}
		if cfg.WebhookSecret == "" {
			return fmt.Errorf("WEBHOOK_SECRET is required when WEBHOOK_CALLBACK_URL is set")
		}
		if len(cfg.WebhookSecret) < 10 || len(cfg.WebhookSecret) > 100 {
			return fmt.Errorf("WEBHOOK_SECRET must be between 10 and 100 characters")
		}
		if cfg.BotUserID == "" {
			return fmt.Errorf("BOT_USER_ID is required when WEBHOOK_CALLBACK_URL is set")
		}
	}

	if cfg.TokenEncryptionKey != "" {
		keyBytes, err := hex.DecodeString(cfg.TokenEncryptionKey)
		if err != nil {
			return fmt.Errorf("TOKEN_ENCRYPTION_KEY must be valid hex: %w", err)
		}
		if len(keyBytes) != 32 {
			return fmt.Errorf("TOKEN_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes), got %d bytes", len(keyBytes))
		}
	}

	return nil
}
