package domain

import (
	"context"
	"time"

	"github.com/pscheid92/uuid"
)

type Config struct {
	UserID    uuid.UUID
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time

	ForTrigger     string
	AgainstTrigger string

	LeftLabel  string
	RightLabel string

	DecaySpeed float64
}

type ConfigSnapshot struct {
	Version        int
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
}

type ConfigRepository interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*Config, error)
	GetByBroadcasterID(ctx context.Context, broadcasterID string) (*Config, error)
	Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, version int) error
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
