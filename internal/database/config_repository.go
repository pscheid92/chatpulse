package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/database/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
)

type ConfigRepo struct {
	q *sqlcgen.Queries
}

func NewConfigRepo(pool *pgxpool.Pool) *ConfigRepo {
	return &ConfigRepo{q: sqlcgen.New(pool)}
}

func (r *ConfigRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	row, err := r.q.GetConfigByUserID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get config by user ID: %w", err)
	}
	return &domain.Config{
		UserID:         row.UserID,
		ForTrigger:     row.ForTrigger,
		AgainstTrigger: row.AgainstTrigger,
		LeftLabel:      row.LeftLabel,
		RightLabel:     row.RightLabel,
		DecaySpeed:     row.DecaySpeed,
		Version:        int(row.Version),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (r *ConfigRepo) GetByBroadcasterID(ctx context.Context, broadcasterID string) (*domain.Config, error) {
	row, err := r.q.GetConfigByBroadcasterID(ctx, broadcasterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get config by broadcaster ID: %w", err)
	}
	return &domain.Config{
		UserID:         row.UserID,
		ForTrigger:     row.ForTrigger,
		AgainstTrigger: row.AgainstTrigger,
		LeftLabel:      row.LeftLabel,
		RightLabel:     row.RightLabel,
		DecaySpeed:     row.DecaySpeed,
		Version:        int(row.Version),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (r *ConfigRepo) Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	tag, err := r.q.UpdateConfig(ctx, sqlcgen.UpdateConfigParams{
		ForTrigger:     forTrigger,
		AgainstTrigger: againstTrigger,
		LeftLabel:      leftLabel,
		RightLabel:     rightLabel,
		DecaySpeed:     decaySpeed,
		UserID:         userID,
	})
	if err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConfigNotFound
	}
	return nil
}
