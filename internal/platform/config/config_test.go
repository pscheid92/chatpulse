package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("TWITCH_CLIENT_ID", "test-client-id")
	t.Setenv("TWITCH_CLIENT_SECRET", "test-client-secret")
	t.Setenv("TWITCH_REDIRECT_URI", "http://localhost:8080/auth/callback")
	t.Setenv("SESSION_SECRET", "test-session-secret")
	t.Setenv("TOKEN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("WEBHOOK_CALLBACK_URL", "https://example.com/webhooks/eventsub")
	t.Setenv("WEBHOOK_SECRET", "test-webhook-secret-at-least-10")
	t.Setenv("BOT_USER_ID", "12345")
}

func TestLoad_AllRequiredVarsSet(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "postgres://localhost/test", cfg.DatabaseURL)
	assert.Equal(t, "test-client-id", cfg.TwitchClientID)
	assert.Equal(t, "test-client-secret", cfg.TwitchClientSecret)
	assert.Equal(t, "http://localhost:8080/auth/callback", cfg.TwitchRedirectURI)
	assert.Equal(t, "test-session-secret", cfg.SessionSecret)
}

func TestLoad_MissingRequired(t *testing.T) {
	tests := []struct {
		name    string
		skipEnv string
		wantErr string
	}{
		{"missing DATABASE_URL", "DATABASE_URL", "DATABASE_URL is required"},
		{"missing TWITCH_CLIENT_ID", "TWITCH_CLIENT_ID", "TWITCH_CLIENT_ID is required"},
		{"missing TWITCH_CLIENT_SECRET", "TWITCH_CLIENT_SECRET", "TWITCH_CLIENT_SECRET is required"},
		{"missing TWITCH_REDIRECT_URI", "TWITCH_REDIRECT_URI", "TWITCH_REDIRECT_URI is required"},
		{"missing SESSION_SECRET", "SESSION_SECRET", "SESSION_SECRET is required"},
		{"missing REDIS_URL", "REDIS_URL", "REDIS_URL is required"},
		{"missing WEBHOOK_CALLBACK_URL", "WEBHOOK_CALLBACK_URL", "WEBHOOK_CALLBACK_URL is required"},
		{"missing WEBHOOK_SECRET", "WEBHOOK_SECRET", "WEBHOOK_SECRET is required"},
		{"missing BOT_USER_ID", "BOT_USER_ID", "BOT_USER_ID is required"},
		{"missing TOKEN_ENCRYPTION_KEY", "TOKEN_ENCRYPTION_KEY", "TOKEN_ENCRYPTION_KEY is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tt.skipEnv, "")

			_, err := Load()
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

func TestLoad_DefaultValues(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "development", cfg.AppEnv)
	assert.Equal(t, "8080", cfg.Port)
}

func TestLoad_CustomPortAndEnv(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PORT", "9090")
	t.Setenv("APP_ENV", "production")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "production", cfg.AppEnv)
	assert.Equal(t, "9090", cfg.Port)
}

func TestLoad_ValidEncryptionKey(t *testing.T) {
	setRequiredEnv(t)
	// 64 hex chars = 32 bytes
	t.Setenv("TOKEN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", cfg.TokenEncryptionKey)
}

func TestLoad_InvalidEncryptionKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{"invalid hex", "not-valid-hex", "TOKEN_ENCRYPTION_KEY must be valid hex"},
		{"too short", "0123456789abcdef", "TOKEN_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("TOKEN_ENCRYPTION_KEY", tt.key)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
