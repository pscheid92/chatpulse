package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Config struct {
	UserID         uuid.UUID
	ForTrigger     string
	AgainstTrigger string
	// TODO: Rename to ForLabel and AgainstLabel perhaps?
	LeftLabel  string
	RightLabel string
	DecaySpeed float64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ConfigSnapshot struct {
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
}

type ConfigRepository interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*Config, error)
	Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
}
