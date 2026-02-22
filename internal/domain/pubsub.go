package domain

import (
	"context"
)

// EventPublisher publishes domain events to infrastructure.
type EventPublisher interface {
	PublishSentimentUpdated(ctx context.Context, broadcasterID string, snapshot *WindowSnapshot) error
	PublishConfigChanged(ctx context.Context, broadcasterID string) error
}
