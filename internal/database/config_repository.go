package database

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// ConfigRepo implements domain.ConfigRepository backed by PostgreSQL.
type ConfigRepo struct {
	db *sql.DB
}

// NewConfigRepo creates a ConfigRepo from the shared DB connection.
func NewConfigRepo(db *DB) *ConfigRepo {
	return &ConfigRepo{db: db.DB}
}

func (r *ConfigRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	var config domain.Config
	err := r.db.QueryRowContext(ctx, `
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

func (r *ConfigRepo) Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE configs
		SET for_trigger = $1, against_trigger = $2, left_label = $3, right_label = $4, decay_speed = $5, updated_at = NOW()
		WHERE user_id = $6
	`, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed, userID)

	return err
}
