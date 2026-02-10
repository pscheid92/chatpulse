package database

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pscheid92/chatpulse/internal/database/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
)

type ConfigRepo struct {
	q *sqlcgen.Queries
}

func NewConfigRepo(db *DB) *ConfigRepo {
	return &ConfigRepo{q: sqlcgen.New(db.Pool)}
}

func (r *ConfigRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	row, err := r.q.GetConfigByUserID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConfigNotFound
	}
	if err != nil {
		return nil, err
	}
	return &domain.Config{
		UserID:         row.UserID,
		ForTrigger:     row.ForTrigger,
		AgainstTrigger: row.AgainstTrigger,
		LeftLabel:      row.LeftLabel,
		RightLabel:     row.RightLabel,
		DecaySpeed:     row.DecaySpeed,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (r *ConfigRepo) Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	return r.q.UpdateConfig(ctx, sqlcgen.UpdateConfigParams{
		ForTrigger:     forTrigger,
		AgainstTrigger: againstTrigger,
		LeftLabel:      leftLabel,
		RightLabel:     rightLabel,
		DecaySpeed:     decaySpeed,
		UserID:         userID,
	})
}
