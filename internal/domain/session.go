package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type SessionUpdate struct {
	Value  float64 `json:"value"`
	Status string  `json:"status"`
}

type SessionRepository interface {
	// Session lifecycle

	ActivateSession(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config ConfigSnapshot) error
	ResumeSession(ctx context.Context, sessionUUID uuid.UUID) error
	SessionExists(ctx context.Context, sessionUUID uuid.UUID) (bool, error)
	DeleteSession(ctx context.Context, sessionUUID uuid.UUID) error
	MarkDisconnected(ctx context.Context, sessionUUID uuid.UUID) error

	// Session queries

	GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*ConfigSnapshot, error)
	UpdateConfig(ctx context.Context, sessionUUID uuid.UUID, config ConfigSnapshot) error

	// Ref counting

	IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error)
	DecrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error)

	// Orphan cleanup

	ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error)
}
