package domain

import (
	"context"
	"time"

	"github.com/pscheid92/uuid"
)

type User struct {
	ID          uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
	OverlayUUID uuid.UUID

	TwitchUserID   string
	TwitchUsername string

	AccessToken  string
	RefreshToken string
	TokenExpiry  time.Time
}

type UserRepository interface {
	GetByID(ctx context.Context, userID uuid.UUID) (*User, error)
	GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*User, error)
	Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*User, error)
	UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}
