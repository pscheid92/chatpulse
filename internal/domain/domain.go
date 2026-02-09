package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// --- Model types ---

type User struct {
	ID             uuid.UUID `db:"id"`
	OverlayUUID    uuid.UUID `db:"overlay_uuid"`
	TwitchUserID   string    `db:"twitch_user_id"`
	TwitchUsername string    `db:"twitch_username"`
	AccessToken    string    `db:"access_token"`
	RefreshToken   string    `db:"refresh_token"`
	TokenExpiry    time.Time `db:"token_expiry"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

type Config struct {
	UserID         uuid.UUID `db:"user_id"`
	ForTrigger     string    `db:"for_trigger"`
	AgainstTrigger string    `db:"against_trigger"`
	LeftLabel      string    `db:"left_label"`
	RightLabel     string    `db:"right_label"`
	DecaySpeed     float64   `db:"decay_speed"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

type ConfigSnapshot struct {
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
}

type EventSubSubscription struct {
	UserID            uuid.UUID `db:"user_id"`
	BroadcasterUserID string    `db:"broadcaster_user_id"`
	SubscriptionID    string    `db:"subscription_id"`
	ConduitID         string    `db:"conduit_id"`
	CreatedAt         time.Time `db:"created_at"`
}

// --- Shared value types ---

// SessionUpdate represents a sentiment state update for a session.
type SessionUpdate struct {
	Value  float64 `json:"value"`
	Status string  `json:"status"`
}

// --- Interfaces ---

// SessionStateStore abstracts session state storage backed by Redis.
type SessionStateStore interface {
	ActivateSession(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config ConfigSnapshot) error
	ResumeSession(ctx context.Context, sessionUUID uuid.UUID) error
	SessionExists(ctx context.Context, sessionUUID uuid.UUID) (bool, error)
	DeleteSession(ctx context.Context, sessionUUID uuid.UUID) error

	GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*ConfigSnapshot, error)
	GetSessionValue(ctx context.Context, sessionUUID uuid.UUID) (float64, bool, error)

	CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error)
	ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error)
	GetDecayedValue(ctx context.Context, sessionUUID uuid.UUID, decayRate float64, nowMs int64) (float64, error)

	ResetValue(ctx context.Context, sessionUUID uuid.UUID) error
	MarkDisconnected(ctx context.Context, sessionUUID uuid.UUID) error
	UpdateConfig(ctx context.Context, sessionUUID uuid.UUID, config ConfigSnapshot) error
	ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error)

	IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error)
	DecrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error)
}

// VoteStore is the subset of SessionStateStore needed for vote processing.
type VoteStore interface {
	GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*ConfigSnapshot, error)
	CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error)
	ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error)
}

// ScaleProvider reads the current decayed sentiment value for a session.
type ScaleProvider interface {
	GetCurrentValue(ctx context.Context, sessionUUID uuid.UUID) (float64, error)
}

// UserRepository abstracts user persistence.
type UserRepository interface {
	GetByID(ctx context.Context, userID uuid.UUID) (*User, error)
	GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*User, error)
	Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*User, error)
	UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

// ConfigRepository abstracts config persistence.
type ConfigRepository interface {
	GetByUserID(ctx context.Context, userID uuid.UUID) (*Config, error)
	Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
}

// EventSubRepository abstracts EventSub subscription persistence.
type EventSubRepository interface {
	Create(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error
	GetByUserID(ctx context.Context, userID uuid.UUID) (*EventSubSubscription, error)
	Delete(ctx context.Context, userID uuid.UUID) error
	List(ctx context.Context) ([]EventSubSubscription, error)
}

// AppService is the application layer contract — handlers route all operations through here.
type AppService interface {
	GetUserByID(ctx context.Context, userID uuid.UUID) (*User, error)
	GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*User, error)
	GetConfig(ctx context.Context, userID uuid.UUID) (*Config, error)
	UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*User, error)
	EnsureSessionActive(ctx context.Context, overlayUUID uuid.UUID) error
	IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) error
	OnSessionEmpty(ctx context.Context, sessionUUID uuid.UUID)
	ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error
	SaveConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, overlayUUID uuid.UUID) error
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

// TwitchService manages EventSub subscriptions.
type TwitchService interface {
	Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error
	Unsubscribe(ctx context.Context, userID uuid.UUID) error
}
