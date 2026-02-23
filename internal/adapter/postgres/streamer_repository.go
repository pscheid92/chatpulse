package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/adapter/postgres/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
)

type StreamerRepo struct {
	pool *pgxpool.Pool
	q    *sqlcgen.Queries
}

func NewStreamerRepo(pool *pgxpool.Pool) *StreamerRepo {
	return &StreamerRepo{
		pool: pool,
		q:    sqlcgen.New(pool),
	}
}

func toDomainStreamer(row sqlcgen.Streamer) *domain.Streamer {
	return &domain.Streamer{
		ID:             row.ID,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		OverlayUUID:    row.OverlayUUID,
		TwitchUserID:   row.TwitchUserID,
		TwitchUsername: row.TwitchUsername,
	}
}

func (r *StreamerRepo) GetByID(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error) {
	row, err := r.q.GetStreamerByID(ctx, streamerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrStreamerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get streamer by ID: %w", err)
	}
	return toDomainStreamer(row), nil
}

func (r *StreamerRepo) GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error) {
	row, err := r.q.GetStreamerByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrStreamerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get streamer by overlay UUID: %w", err)
	}
	return toDomainStreamer(row), nil
}

func (r *StreamerRepo) Upsert(ctx context.Context, twitchUserID, twitchUsername string) (*domain.Streamer, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.q.WithTx(tx)
	row, err := qtx.UpsertStreamer(ctx, sqlcgen.UpsertStreamerParams{
		TwitchUserID:   twitchUserID,
		TwitchUsername: twitchUsername,
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

	return toDomainStreamer(row), nil
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
