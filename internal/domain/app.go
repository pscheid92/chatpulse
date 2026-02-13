package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type AppService interface {
	GetUserByID(ctx context.Context, userID uuid.UUID) (*User, error)
	GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*User, error)
	GetConfig(ctx context.Context, userID uuid.UUID) (*Config, error)
	UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*User, error)
	ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error
	SaveConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, broadcasterID string) error
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}
