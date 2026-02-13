package coordination

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// ConfigInvalidator subscribes to config invalidation messages and
// invalidates the local config cache when changes are broadcast.
type ConfigInvalidator struct {
	redis      *redis.Client
	invalidate func(broadcasterID string)
}

// NewConfigInvalidator creates a new config invalidator.
// The invalidate function is called with the broadcaster_id from each invalidation message.
func NewConfigInvalidator(redis *redis.Client, invalidate func(broadcasterID string)) *ConfigInvalidator {
	return &ConfigInvalidator{
		redis:      redis,
		invalidate: invalidate,
	}
}

// Start begins listening for config invalidation messages.
// Blocks until ctx is cancelled.
func (c *ConfigInvalidator) Start(ctx context.Context) {
	pubsub := c.redis.Subscribe(ctx, "config:invalidate")
	defer func() {
		_ = pubsub.Close()
	}()

	ch := pubsub.Channel()
	for {
		select {
		case msg := <-ch:
			if msg == nil {
				return
			}
			c.handleInvalidation(msg.Payload)
		case <-ctx.Done():
			return
		}
	}
}

// handleInvalidation processes a single invalidation message.
// The payload is the broadcaster_id (string) to invalidate.
func (c *ConfigInvalidator) handleInvalidation(payload string) {
	metrics.PubSubMessagesReceived.WithLabelValues("config:invalidate").Inc()

	if payload == "" {
		slog.Warn("Empty config invalidation message")
		return
	}

	c.invalidate(payload)
	slog.Debug("Config cache invalidated via pub/sub",
		"broadcaster_id", payload)
}

// PublishConfigInvalidation broadcasts a config invalidation message to all instances.
// The payload is the broadcaster_id (string) to invalidate.
func PublishConfigInvalidation(ctx context.Context, redis *redis.Client, broadcasterID string) error {
	if err := redis.Publish(ctx, "config:invalidate", broadcasterID).Err(); err != nil {
		return fmt.Errorf("failed to publish config invalidation: %w", err)
	}
	return nil
}
