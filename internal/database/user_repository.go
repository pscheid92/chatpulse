package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/crypto"
	"github.com/pscheid92/chatpulse/internal/database/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
)

type UserRepo struct {
	pool   *pgxpool.Pool
	q      *sqlcgen.Queries
	crypto crypto.Service
}

func NewUserRepo(pool *pgxpool.Pool, crypto crypto.Service) *UserRepo {
	return &UserRepo{
		pool:   pool,
		q:      sqlcgen.New(pool),
		crypto: crypto,
	}
}

func (r *UserRepo) toDomainUser(row sqlcgen.User) (*domain.User, error) {
	accessToken, err := r.crypto.Decrypt(row.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt access token: %w", err)
	}

	refreshToken, err := r.crypto.Decrypt(row.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	user := domain.User{
		ID:             row.ID,
		OverlayUUID:    row.OverlayUUID,
		TwitchUserID:   row.TwitchUserID,
		TwitchUsername: row.TwitchUsername,
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		TokenExpiry:    row.TokenExpiry,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	return &user, nil
}

func (r *UserRepo) GetByID(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	row, err := r.q.GetUserByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.toDomainUser(row)
}

func (r *UserRepo) GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
	row, err := r.q.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.toDomainUser(row)
}

func (r *UserRepo) Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error) {
	encAccessToken, err := r.crypto.Encrypt(accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt access token: %w", err)
	}

	encRefreshToken, err := r.crypto.Encrypt(refreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.q.WithTx(tx)
	row, err := qtx.UpsertUser(ctx, sqlcgen.UpsertUserParams{
		TwitchUserID:   twitchUserID,
		TwitchUsername: twitchUsername,
		AccessToken:    encAccessToken,
		RefreshToken:   encRefreshToken,
		TokenExpiry:    tokenExpiry,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert user: %w", err)
	}

	if err := qtx.InsertDefaultConfig(ctx, row.ID); err != nil {
		return nil, fmt.Errorf("failed to create default config: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return r.toDomainUser(row)
}

func (r *UserRepo) UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error {
	encAccessToken, err := r.crypto.Encrypt(accessToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt access token: %w", err)
	}

	encRefreshToken, err := r.crypto.Encrypt(refreshToken)
	if err != nil {
		return fmt.Errorf("failed to encrypt refresh token: %w", err)
	}

	tag, err := r.q.UpdateTokens(ctx, sqlcgen.UpdateTokensParams{
		AccessToken:  encAccessToken,
		RefreshToken: encRefreshToken,
		TokenExpiry:  tokenExpiry,
		ID:           userID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

func (r *UserRepo) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	newUUID, err := r.q.RotateOverlayUUID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrUserNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to rotate overlay UUID: %w", err)
	}
	return newUUID, nil
}
