package sentiment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/models"
)

// SessionStateStore abstracts session state storage.
// In-memory implementation is used for single-instance mode.
// Redis implementation enables horizontal scaling across multiple instances.
type SessionStateStore interface {
	ActivateSession(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config models.ConfigSnapshot) error
	ResumeSession(ctx context.Context, sessionUUID uuid.UUID) error
	SessionExists(ctx context.Context, sessionUUID uuid.UUID) (bool, error)
	DeleteSession(ctx context.Context, sessionUUID uuid.UUID) error

	GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*models.ConfigSnapshot, error)
	GetSessionValue(ctx context.Context, sessionUUID uuid.UUID) (float64, bool, error)

	CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error)
	ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta float64) (float64, error)
	ApplyDecay(ctx context.Context, sessionUUID uuid.UUID, decayFactor float64, nowMs int64, minIntervalMs int64) (float64, error)

	ResetValue(ctx context.Context, sessionUUID uuid.UUID) error
	MarkDisconnected(ctx context.Context, sessionUUID uuid.UUID) error
	UpdateConfig(ctx context.Context, sessionUUID uuid.UUID, config models.ConfigSnapshot) error
	ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error)

	// PruneDebounce removes stale debounce entries. No-op for Redis (TTL handles it).
	PruneDebounce(ctx context.Context) error

	// NeedsBroadcast returns true if the Engine should call Broadcast after state changes.
	// In-memory: true (no Pub/Sub). Redis: false (Lua scripts publish via Pub/Sub).
	NeedsBroadcast() bool
}
