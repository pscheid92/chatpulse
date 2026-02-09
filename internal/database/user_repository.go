package database

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// userColumns must match the Scan order in scanUser.
const userColumns = `id, overlay_uuid, twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at`

// UserRepo implements domain.UserRepository backed by PostgreSQL.
type UserRepo struct {
	db  *sql.DB
	gcm cipher.AEAD
}

// NewUserRepo creates a UserRepo from the shared DB connection.
func NewUserRepo(db *DB) *UserRepo {
	return &UserRepo{db: db.DB, gcm: db.gcm}
}

func (r *UserRepo) encryptToken(plaintext string) (string, error) {
	if r.gcm == nil {
		return plaintext, nil
	}

	nonce := make([]byte, r.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := r.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

func (r *UserRepo) decryptToken(encoded string) (string, error) {
	if r.gcm == nil {
		return encoded, nil
	}

	ciphertext, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("failed to decode hex: %w", err)
	}

	nonceSize := r.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := r.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

func (r *UserRepo) decryptUserTokens(user *domain.User) error {
	var err error
	user.AccessToken, err = r.decryptToken(user.AccessToken)
	if err != nil {
		return fmt.Errorf("failed to decrypt access token: %w", err)
	}
	user.RefreshToken, err = r.decryptToken(user.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to decrypt refresh token: %w", err)
	}
	return nil
}

func (r *UserRepo) scanUser(row *sql.Row) (*domain.User, error) {
	var user domain.User
	err := row.Scan(
		&user.ID, &user.OverlayUUID, &user.TwitchUserID, &user.TwitchUsername,
		&user.AccessToken, &user.RefreshToken, &user.TokenExpiry,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := r.decryptUserTokens(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepo) Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error) {
	encAccessToken, err := r.encryptToken(accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt access token: %w", err)
	}
	encRefreshToken, err := r.encryptToken(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	user, err := r.scanUser(tx.QueryRowContext(ctx, `
		INSERT INTO users (twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (twitch_user_id) DO UPDATE SET
			twitch_username = EXCLUDED.twitch_username,
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			token_expiry = EXCLUDED.token_expiry,
			updated_at = NOW()
		RETURNING `+userColumns+`
	`, twitchUserID, twitchUsername, encAccessToken, encRefreshToken, tokenExpiry))
	if err != nil {
		return nil, fmt.Errorf("failed to upsert user: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO configs (user_id, created_at, updated_at)
		VALUES ($1, NOW(), NOW())
		ON CONFLICT (user_id) DO NOTHING
	`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create default config: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return user, nil
}

func (r *UserRepo) GetByID(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	return r.scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, userID))
}

func (r *UserRepo) UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error {
	encAccessToken, err := r.encryptToken(accessToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt access token: %w", err)
	}
	encRefreshToken, err := r.encryptToken(refreshToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	_, err = r.db.ExecContext(ctx, `
		UPDATE users
		SET access_token = $1, refresh_token = $2, token_expiry = $3, updated_at = NOW()
		WHERE id = $4
	`, encAccessToken, encRefreshToken, tokenExpiry, userID)

	return err
}

func (r *UserRepo) GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
	return r.scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE overlay_uuid = $1`, overlayUUID))
}

func (r *UserRepo) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var newUUID uuid.UUID
	err := r.db.QueryRowContext(ctx, `
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
