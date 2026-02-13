package coordination

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// ConfigInvalidator subscribes to config invalidation messages and
// invalidates the local config cache when changes are broadcast.
type ConfigInvalidator struct {
	redis  *redis.Client
	engine domain.Engine
}

// NewConfigInvalidator creates a new config invalidator.
func NewConfigInvalidator(redis *redis.Client, engine domain.Engine) *ConfigInvalidator {
	return &ConfigInvalidator{
		redis:  redis,
		engine: engine,
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
func (c *ConfigInvalidator) handleInvalidation(payload string) {
	metrics.PubSubMessagesReceived.WithLabelValues("config:invalidate").Inc()

	overlayUUID, err := uuid.Parse(payload)
	if err != nil {
		slog.Warn("Invalid overlay UUID in config invalidation message",
			"payload", payload,
			"error", err)
		return
	}

	c.engine.InvalidateConfigCache(overlayUUID)
	slog.Debug("Config cache invalidated via pub/sub",
		"overlay_uuid", overlayUUID)
}

// PublishConfigInvalidation broadcasts a config invalidation message to all instances.
// This should be called after updating a user's config in the database.
func PublishConfigInvalidation(ctx context.Context, redis *redis.Client, overlayUUID uuid.UUID) error {
	if err := redis.Publish(ctx, "config:invalidate", overlayUUID.String()).Err(); err != nil {
		return fmt.Errorf("failed to publish config invalidation: %w", err)
	}
	return nil
}
