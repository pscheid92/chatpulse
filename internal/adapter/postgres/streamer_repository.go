package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/adapter/postgres/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/crypto"
	"github.com/pscheid92/uuid"
)

type StreamerRepo struct {
	pool   *pgxpool.Pool
	q      *sqlcgen.Queries
	crypto crypto.Service
}

func NewStreamerRepo(pool *pgxpool.Pool, crypto crypto.Service) *StreamerRepo {
	return &StreamerRepo{
		pool:   pool,
		q:      sqlcgen.New(pool),
		crypto: crypto,
	}
}

func (r *StreamerRepo) toDomainStreamer(row sqlcgen.Streamer) (*domain.Streamer, error) {
	accessToken, err := r.crypto.Decrypt(row.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt access token: %w", err)
	}

	refreshToken, err := r.crypto.Decrypt(row.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	streamer := domain.Streamer{
		ID:             row.ID,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		OverlayUUID:    row.OverlayUUID,
		TwitchUserID:   row.TwitchUserID,
		TwitchUsername: row.TwitchUsername,
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		TokenExpiry:    row.TokenExpiry,
	}
	return &streamer, nil
}

func (r *StreamerRepo) GetByID(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error) {
	row, err := r.q.GetStreamerByID(ctx, streamerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrStreamerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get streamer by ID: %w", err)
	}
	return r.toDomainStreamer(row)
}

func (r *StreamerRepo) GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error) {
	row, err := r.q.GetStreamerByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrStreamerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get streamer by overlay UUID: %w", err)
	}
	return r.toDomainStreamer(row)
}

func (r *StreamerRepo) Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.Streamer, error) {
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
	row, err := qtx.UpsertStreamer(ctx, sqlcgen.UpsertStreamerParams{
		TwitchUserID:   twitchUserID,
		TwitchUsername: twitchUsername,
		AccessToken:    encAccessToken,
		RefreshToken:   encRefreshToken,
		TokenExpiry:    tokenExpiry,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upsert streamer: %w", err)
	}

	if err := qtx.InsertDefaultConfig(ctx, row.ID); err != nil {
		return nil, fmt.Errorf("failed to create default config: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return r.toDomainStreamer(row)
}

func (r *StreamerRepo) RotateOverlayUUID(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error) {
	newUUID, err := r.q.RotateOverlayUUID(ctx, streamerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrStreamerNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to rotate overlay UUID: %w", err)
	}
	return newUUID, nil
}
