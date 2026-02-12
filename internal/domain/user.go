package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID             uuid.UUID
	OverlayUUID    uuid.UUID
	TwitchUserID   string
	TwitchUsername string
	// Tokens are kept in User struct for simplicity. Rationale:
	// - User and tokens have identical lifecycle (created/updated together)
	// - No use case for querying users without tokens or vice versa
	// - Separation would add complexity (JOIN queries, dual updates) without clear benefit
	// - Token encryption is handled at repository layer, not domain layer
	AccessToken  string
	RefreshToken string
	TokenExpiry  time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UserRepository interface {
	GetByID(ctx context.Context, userID uuid.UUID) (*User, error)
	GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*User, error)
	Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*User, error)
	UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}
