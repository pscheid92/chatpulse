package domain

import (
	"context"
	"time"

	"github.com/pscheid92/uuid"
)

type EventSubSubscription struct {
	UserID            uuid.UUID
	BroadcasterUserID string
	SubscriptionID    string
	ConduitID         string
	CreatedAt         time.Time
}

type EventSubRepository interface {
	Create(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error
	GetByUserID(ctx context.Context, userID uuid.UUID) (*EventSubSubscription, error)
	Delete(ctx context.Context, userID uuid.UUID) error
	List(ctx context.Context) ([]EventSubSubscription, error)
}

type TwitchService interface {
	Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error
	Unsubscribe(ctx context.Context, userID uuid.UUID) error
}
