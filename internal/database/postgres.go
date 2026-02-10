package database

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	*pgxpool.Pool
	gcm cipher.AEAD // cached AES-256-GCM cipher; nil means no encryption
}

func Connect(ctx context.Context, databaseURL, encryptionKeyHex string) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	d := &DB{Pool: pool}

	if encryptionKeyHex != "" {
		key, err := hex.DecodeString(encryptionKeyHex)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("invalid encryption key hex: %w", err)
		}
		if len(key) != 32 {
			pool.Close()
			return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("failed to create cipher: %w", err)
		}
		aesGCM, err := cipher.NewGCM(block)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("failed to create GCM: %w", err)
		}
		d.gcm = aesGCM
	}

	return d, nil
}

func (db *DB) HealthCheck(ctx context.Context) error {
	return db.Ping(ctx)
}

func (db *DB) RunMigrations(ctx context.Context) error {
	for _, migration := range migrations {
		if _, err := db.Exec(ctx, migration); err != nil {
			return fmt.Errorf("failed to run migration: %w", err)
		}
	}

	log.Println("Database migrations completed successfully")
	return nil
}

var migrations = []string{
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
