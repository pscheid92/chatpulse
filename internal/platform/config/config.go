package config

import (
	"encoding/hex"
	"errors"
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

	MaxWebSocketConnections int `env:"MAX_WEBSOCKET_CONNECTIONS" default:"10000"`

	SessionMaxAge time.Duration `env:"SESSION_MAX_AGE" default:"168h"` // 7 days
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		slog.Info("No .env file found, using environment variables")
	}

	var cfg Config
	if err := env.Load(&cfg, nil); err != nil {
		return nil, fmt.Errorf("failed to load environment variables: %w", err)
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
		"WEBHOOK_CALLBACK_URL": cfg.WebhookCallbackURL,
		"WEBHOOK_SECRET":       cfg.WebhookSecret,
		"BOT_USER_ID":          cfg.BotUserID,
		"TOKEN_ENCRYPTION_KEY": cfg.TokenEncryptionKey,
	}
	for name, value := range required {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	if len(cfg.WebhookSecret) < 10 || len(cfg.WebhookSecret) > 100 {
		return errors.New("WEBHOOK_SECRET must be between 10 and 100 characters")
	}

	keyBytes, err := hex.DecodeString(cfg.TokenEncryptionKey)
	if err != nil {
		return fmt.Errorf("TOKEN_ENCRYPTION_KEY must be valid hex: %w", err)
	}
	if len(keyBytes) != 32 {
		return fmt.Errorf("TOKEN_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes), got %d bytes", len(keyBytes))
	}

	return nil
}
