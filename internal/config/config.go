package config

import (
	"encoding/hex"
	"fmt"
	"log"

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
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
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
