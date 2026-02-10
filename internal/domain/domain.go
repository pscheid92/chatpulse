package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors for not-found lookups.
var (
	ErrUserNotFound         = errors.New("user not found")
	ErrConfigNotFound       = errors.New("config not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
)

// --- Model types ---

type User struct {
	ID             uuid.UUID
	OverlayUUID    uuid.UUID
	TwitchUserID   string
	TwitchUsername string
	AccessToken    string
	RefreshToken   string
	TokenExpiry    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Config struct {
	UserID         uuid.UUID
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ConfigSnapshot struct {
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
}

type EventSubSubscription struct {
	UserID            uuid.UUID
	BroadcasterUserID string
	SubscriptionID    string
	ConduitID         string
	CreatedAt         time.Time
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
