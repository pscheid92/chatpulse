package database

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"
)

type DB struct {
	*sql.DB
	gcm cipher.AEAD // cached AES-256-GCM cipher; nil means no encryption
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

	if err := db.PingContext(context.Background()); err != nil {
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
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("failed to create cipher: %w", err)
		}
		aesGCM, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("failed to create GCM: %w", err)
		}
		d.gcm = aesGCM
	}

	return d, nil
}

func (db *DB) HealthCheck(ctx context.Context) error {
	return db.PingContext(ctx)
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
		`CREATE TABLE IF NOT EXISTS eventsub_subscriptions (
			user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			broadcaster_user_id TEXT NOT NULL,
			subscription_id TEXT NOT NULL,
			conduit_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW()
		)`,
	}

	ctx := context.Background()
	for _, migration := range migrations {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("failed to run migration: %w", err)
		}
	}

	log.Println("Database migrations completed successfully")
	return nil
}
