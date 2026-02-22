package domain

import (
	"context"
	"time"

	"github.com/pscheid92/uuid"
)

type Streamer struct {
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

type StreamerRepository interface {
	GetByID(ctx context.Context, streamerID uuid.UUID) (*Streamer, error)
	GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*Streamer, error)
	Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*Streamer, error)
	RotateOverlayUUID(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error)
}
