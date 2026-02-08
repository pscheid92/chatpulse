package config

import (
	"encoding/hex"
	"fmt"
	"os"
)

type Config struct {
	AppEnv             string
	Port               string
	DatabaseURL        string
	TwitchClientID     string
	TwitchClientSecret string
	TwitchRedirectURI  string
	SessionSecret      string
	TokenEncryptionKey string
	WebhookCallbackURL string
	WebhookSecret      string
	BotUserID          string
	RedisURL           string
}

func Load() (*Config, error) {
	cfg := &Config{
		AppEnv:             getEnv("APP_ENV", "development"),
		Port:               getEnv("PORT", "8080"),
		DatabaseURL:        getEnv("DATABASE_URL", ""),
		TwitchClientID:     getEnv("TWITCH_CLIENT_ID", ""),
		TwitchClientSecret: getEnv("TWITCH_CLIENT_SECRET", ""),
		TwitchRedirectURI:  getEnv("TWITCH_REDIRECT_URI", ""),
		SessionSecret:      getEnv("SESSION_SECRET", ""),
		TokenEncryptionKey: getEnv("TOKEN_ENCRYPTION_KEY", ""),
		WebhookCallbackURL: getEnv("WEBHOOK_CALLBACK_URL", ""),
		WebhookSecret:      getEnv("WEBHOOK_SECRET", ""),
		BotUserID:          getEnv("BOT_USER_ID", ""),
		RedisURL:           getEnv("REDIS_URL", ""),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.TwitchClientID == "" {
		return nil, fmt.Errorf("TWITCH_CLIENT_ID is required")
	}
	if cfg.TwitchClientSecret == "" {
		return nil, fmt.Errorf("TWITCH_CLIENT_SECRET is required")
	}
	if cfg.TwitchRedirectURI == "" {
		return nil, fmt.Errorf("TWITCH_REDIRECT_URI is required")
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}

	// Webhook config: all three must be set together
	if cfg.WebhookCallbackURL != "" || cfg.WebhookSecret != "" {
		if cfg.WebhookCallbackURL == "" {
			return nil, fmt.Errorf("WEBHOOK_CALLBACK_URL is required when WEBHOOK_SECRET is set")
		}
		if cfg.WebhookSecret == "" {
			return nil, fmt.Errorf("WEBHOOK_SECRET is required when WEBHOOK_CALLBACK_URL is set")
		}
		if len(cfg.WebhookSecret) < 10 || len(cfg.WebhookSecret) > 100 {
			return nil, fmt.Errorf("WEBHOOK_SECRET must be between 10 and 100 characters")
		}
		if cfg.BotUserID == "" {
			return nil, fmt.Errorf("BOT_USER_ID is required when WEBHOOK_CALLBACK_URL is set")
		}
	}

	if cfg.TokenEncryptionKey != "" {
		keyBytes, err := hex.DecodeString(cfg.TokenEncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("TOKEN_ENCRYPTION_KEY must be valid hex: %w", err)
		}
		if len(keyBytes) != 32 {
			return nil, fmt.Errorf("TOKEN_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes), got %d bytes", len(keyBytes))
		}
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
