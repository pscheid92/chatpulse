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
	Version    int // Incremented on each update for drift detection
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ConfigSnapshot struct {
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
	Version        int // Cached version for drift detection
}

type ConfigRepository interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*Config, error)
	GetByBroadcasterID(ctx context.Context, broadcasterID string) (*Config, error)
	Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
}

// ConfigSource provides config lookup by broadcaster ID with caching.
// Implementations should provide read-through caching (e.g., Redis → PostgreSQL).
type ConfigSource interface {
	GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (*ConfigSnapshot, error)
}

// ConfigCacheInvalidator removes a broadcaster's config from the Redis cache.
type ConfigCacheInvalidator interface {
	InvalidateCache(ctx context.Context, broadcasterID string) error
}
