package domain

import (
	"context"

	"github.com/pscheid92/uuid"
)

type SessionUpdate struct {
	Value      float64 `json:"value"`
	DecaySpeed float64 `json:"decaySpeed"`
	Timestamp  int64   `json:"timestamp"`
	Status     string  `json:"status"`
}

type SessionRepository interface {
	GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	GetBroadcasterID(ctx context.Context, sessionUUID uuid.UUID) (string, error)
	GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*ConfigSnapshot, error)
}
