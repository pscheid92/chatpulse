package domain

import (
	"context"
	"time"

	"github.com/pscheid92/uuid"
)

// EventSubSubscription is a record of a Twitch EventSub subscription
// linking a streamer to a conduit-based webhook delivery.
type EventSubSubscription struct {
	StreamerID        uuid.UUID
	BroadcasterUserID string
	SubscriptionID    string
	ConduitID         string
	CreatedAt         time.Time
}

// EventSubService manages Twitch EventSub subscriptions for streamers.
type EventSubService interface {
	Subscribe(ctx context.Context, streamerID uuid.UUID, broadcasterUserID string) error
	Unsubscribe(ctx context.Context, streamerID uuid.UUID) error
}

// EventSubRepository persists EventSub subscription records.
type EventSubRepository interface {
	Create(ctx context.Context, streamerID uuid.UUID, subscriptionID, conduitID string) error
	GetByStreamerID(ctx context.Context, streamerID uuid.UUID) (*EventSubSubscription, error)
	Delete(ctx context.Context, streamerID uuid.UUID) error
	DeleteByConduitID(ctx context.Context, conduitID string) error
	List(ctx context.Context) ([]EventSubSubscription, error)
}
