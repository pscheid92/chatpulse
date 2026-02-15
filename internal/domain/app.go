package domain

import (
	"context"
	"time"

	"github.com/pscheid92/uuid"
)

// UserService handles user lookup and creation.
type UserService interface {
	GetUserByID(ctx context.Context, userID uuid.UUID) (*User, error)
	GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*User, error)
	UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*User, error)
}

// SaveConfigRequest bundles all parameters for a config save operation.
type SaveConfigRequest struct {
	UserID         uuid.UUID
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
	BroadcasterID  string
}

// ConfigService handles config reads/writes, overlay UUID rotation, and sentiment resets.
type ConfigService interface {
	GetConfig(ctx context.Context, userID uuid.UUID) (*Config, error)
	SaveConfig(ctx context.Context, req SaveConfigRequest) error
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
	ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error
}
