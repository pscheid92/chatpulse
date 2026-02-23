package domain

import (
	"context"

	"github.com/pscheid92/uuid"
)

type OverlayConfig struct {
	MemorySeconds int

	ForTrigger     string
	ForLabel       string
	AgainstTrigger string
	AgainstLabel   string

	DisplayMode DisplayMode
	Theme       Theme
}

// OverlayConfigWithVersion wraps OverlayConfig with an optimistic-locking version.
type OverlayConfigWithVersion struct {
	OverlayConfig
	Version int
}

// ConfigSource provides config lookup by broadcaster ID, typically with caching.
type ConfigSource interface {
	GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (OverlayConfig, error)
}

type ConfigRepository interface {
	GetByStreamerID(ctx context.Context, streamerID uuid.UUID) (*OverlayConfigWithVersion, error)
	GetByBroadcasterID(ctx context.Context, broadcasterID string) (*OverlayConfigWithVersion, error)
	Update(ctx context.Context, streamerID uuid.UUID, config OverlayConfig, version int) error
}
