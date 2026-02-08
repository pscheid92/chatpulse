package database

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/pscheid92/twitch-tow/internal/models"
)

type DB struct {
	*sql.DB
	encryptionKey []byte // 32 bytes for AES-256; nil means no encryption
}

func Connect(databaseURL, encryptionKeyHex string) (*DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Connection pool settings for production use
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	d := &DB{DB: db}

	if encryptionKeyHex != "" {
		key, err := hex.DecodeString(encryptionKeyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid encryption key hex: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
		}
		d.encryptionKey = key
	}

	return d, nil
}

func (db *DB) HealthCheck(ctx context.Context) error {
	return db.PingContext(ctx)
}

func (db *DB) encryptToken(plaintext string) (string, error) {
	if db.encryptionKey == nil {
		return plaintext, nil
	}

	block, err := aes.NewCipher(db.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

func (db *DB) decryptToken(encoded string) (string, error) {
	if db.encryptionKey == nil {
		return encoded, nil
	}

	ciphertext, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode hex: %w", err)
	}

	block, err := aes.NewCipher(db.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

func (db *DB) RunMigrations() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			twitch_user_id TEXT UNIQUE NOT NULL,
			twitch_username TEXT NOT NULL,
			access_token TEXT NOT NULL,
			refresh_token TEXT NOT NULL,
			token_expiry TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_twitch_user_id ON users(twitch_user_id)`,
		`CREATE TABLE IF NOT EXISTS configs (
			user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			for_trigger TEXT NOT NULL DEFAULT 'yes',
			against_trigger TEXT NOT NULL DEFAULT 'no',
			left_label TEXT NOT NULL DEFAULT 'Against',
			right_label TEXT NOT NULL DEFAULT 'For',
			decay_speed FLOAT NOT NULL DEFAULT 0.5,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS overlay_uuid UUID UNIQUE DEFAULT gen_random_uuid()`,
		`CREATE INDEX IF NOT EXISTS idx_users_overlay_uuid ON users(overlay_uuid)`,
	}

	for _, migration := range migrations {
		if _, err := db.Exec(migration); err != nil {
			return fmt.Errorf("failed to run migration: %w", err)
		}
	}

	log.Println("Database migrations completed successfully")
	return nil
}

func (db *DB) UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*models.User, error) {
	encAccessToken, err := db.encryptToken(accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt access token: %w", err)
	}
	encRefreshToken, err := db.encryptToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	var user models.User

	err = db.QueryRowContext(ctx, `
		INSERT INTO users (twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (twitch_user_id) DO UPDATE SET
			twitch_username = EXCLUDED.twitch_username,
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			token_expiry = EXCLUDED.token_expiry,
			updated_at = NOW()
		RETURNING id, overlay_uuid, twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at
	`, twitchUserID, twitchUsername, encAccessToken, encRefreshToken, tokenExpiry).Scan(
		&user.ID, &user.OverlayUUID, &user.TwitchUserID, &user.TwitchUsername, &user.AccessToken,
		&user.RefreshToken, &user.TokenExpiry, &user.CreatedAt, &user.UpdatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to upsert user: %w", err)
	}

	// Decrypt tokens for in-memory use
	user.AccessToken, err = db.decryptToken(user.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt access token: %w", err)
	}
	user.RefreshToken, err = db.decryptToken(user.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	// Ensure user has a default config
	_, err = db.ExecContext(ctx, `
		INSERT INTO configs (user_id, created_at, updated_at)
		VALUES ($1, NOW(), NOW())
		ON CONFLICT (user_id) DO NOTHING
	`, user.ID)

	if err != nil {
		return nil, fmt.Errorf("failed to create default config: %w", err)
	}

	return &user, nil
}

func (db *DB) GetUserByID(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	var user models.User
	err := db.QueryRowContext(ctx, `
		SELECT id, overlay_uuid, twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at
		FROM users
		WHERE id = $1
	`, userID).Scan(
		&user.ID, &user.OverlayUUID, &user.TwitchUserID, &user.TwitchUsername, &user.AccessToken,
		&user.RefreshToken, &user.TokenExpiry, &user.CreatedAt, &user.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	user.AccessToken, err = db.decryptToken(user.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt access token: %w", err)
	}
	user.RefreshToken, err = db.decryptToken(user.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	return &user, nil
}

func (db *DB) UpdateUserTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error {
	encAccessToken, err := db.encryptToken(accessToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt access token: %w", err)
	}
	encRefreshToken, err := db.encryptToken(refreshToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		UPDATE users
		SET access_token = $1, refresh_token = $2, token_expiry = $3, updated_at = NOW()
		WHERE id = $4
	`, encAccessToken, encRefreshToken, tokenExpiry, userID)

	return err
}

func (db *DB) GetConfig(ctx context.Context, userID uuid.UUID) (*models.Config, error) {
	var config models.Config
	err := db.QueryRowContext(ctx, `
		SELECT user_id, for_trigger, against_trigger, left_label, right_label, decay_speed, created_at, updated_at
		FROM configs
		WHERE user_id = $1
	`, userID).Scan(
		&config.UserID, &config.ForTrigger, &config.AgainstTrigger, &config.LeftLabel,
		&config.RightLabel, &config.DecaySpeed, &config.CreatedAt, &config.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	return &config, nil
}

func (db *DB) UpdateConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	_, err := db.ExecContext(ctx, `
		UPDATE configs
		SET for_trigger = $1, against_trigger = $2, left_label = $3, right_label = $4, decay_speed = $5, updated_at = NOW()
		WHERE user_id = $6
	`, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed, userID)

	return err
}

func (db *DB) GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*models.User, error) {
	var user models.User
	err := db.QueryRowContext(ctx, `
		SELECT id, overlay_uuid, twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at
		FROM users
		WHERE overlay_uuid = $1
	`, overlayUUID).Scan(
		&user.ID, &user.OverlayUUID, &user.TwitchUserID, &user.TwitchUsername, &user.AccessToken,
		&user.RefreshToken, &user.TokenExpiry, &user.CreatedAt, &user.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	user.AccessToken, err = db.decryptToken(user.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt access token: %w", err)
	}
	user.RefreshToken, err = db.decryptToken(user.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	return &user, nil
}

func (db *DB) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var newUUID uuid.UUID
	err := db.QueryRowContext(ctx, `
		UPDATE users
		SET overlay_uuid = gen_random_uuid(), updated_at = NOW()
		WHERE id = $1
		RETURNING overlay_uuid
	`, userID).Scan(&newUUID)

	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to rotate overlay UUID: %w", err)
	}

	return newUUID, nil
}
