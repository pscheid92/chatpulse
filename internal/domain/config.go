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
	// Labels use spatial naming (Left/Right) to match overlay positioning. Rationale:
	// - Overlay UI displays labels on left/right sides of sentiment bar (overlay.html:128-129)
	// - Dashboard UI clarifies semantic meaning: "Left Label (Against)" / "Right Label (For)"
	// - Spatial naming matches user mental model when positioning overlay elements
	// - ForLabel/AgainstLabel would be redundant with ForTrigger/AgainstTrigger
	// - CSS classes use .label-left/.label-right for consistency
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
