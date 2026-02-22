package eventpublisher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pscheid92/chatpulse/internal/adapter/redis"
	"github.com/pscheid92/chatpulse/internal/adapter/websocket"
	"github.com/pscheid92/chatpulse/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

// EventPublisher implements domain.EventPublisher by composing the websocket
// publisher and Redis cache invalidation.
type EventPublisher struct {
	sentiment   *websocket.Publisher
	configCache *redis.ConfigCacheRepo
	redisClient *goredis.Client
}

func New(sentiment *websocket.Publisher, configCache *redis.ConfigCacheRepo, redisClient *goredis.Client) *EventPublisher {
	return &EventPublisher{
		sentiment:   sentiment,
		configCache: configCache,
		redisClient: redisClient,
	}
}

func (ep *EventPublisher) PublishSentimentUpdated(ctx context.Context, broadcasterID string, snapshot *domain.WindowSnapshot) error {
	if err := ep.sentiment.PublishSentiment(ctx, broadcasterID, snapshot); err != nil {
		return fmt.Errorf("publish sentiment: %w", err)
	}
	return nil
}

func (ep *EventPublisher) PublishConfigChanged(ctx context.Context, broadcasterID string) error {
	if err := ep.configCache.InvalidateCache(ctx, broadcasterID); err != nil {
		slog.Warn("Failed to invalidate Redis config cache", "broadcaster_id", broadcasterID, "error", err)
	}
	if err := redis.PublishConfigInvalidation(ctx, ep.redisClient, broadcasterID); err != nil {
		slog.Warn("Failed to publish config invalidation", "broadcaster_id", broadcasterID, "error", err)
	}
	return nil
}
