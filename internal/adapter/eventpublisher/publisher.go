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
	sentiment      *websocket.Publisher
	configCache    *redis.ConfigCacheRepo
	redisClient    *goredis.Client
	sentimentStore domain.SentimentStore
	configSource   domain.ConfigSource
}

func New(sentiment *websocket.Publisher, configCache *redis.ConfigCacheRepo, redisClient *goredis.Client, sentimentStore domain.SentimentStore, configSource domain.ConfigSource) *EventPublisher {
	return &EventPublisher{
		sentiment:      sentiment,
		configCache:    configCache,
		redisClient:    redisClient,
		sentimentStore: sentimentStore,
		configSource:   configSource,
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
		slog.WarnContext(ctx, "Failed to invalidate Redis config cache", "broadcaster_id", broadcasterID, "error", err)
	}
	if err := redis.PublishConfigInvalidation(ctx, ep.redisClient, broadcasterID); err != nil {
		slog.WarnContext(ctx, "Failed to publish config invalidation", "broadcaster_id", broadcasterID, "error", err)
	}

	// Push current state to connected overlays so config changes appear immediately.
	config, err := ep.configSource.GetConfigByBroadcaster(ctx, broadcasterID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to fetch config for overlay push", "broadcaster_id", broadcasterID, "error", err)
		return nil
	}
	snapshot, err := ep.sentimentStore.GetSnapshot(ctx, broadcasterID, config.MemorySeconds)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get sentiment snapshot for overlay push", "broadcaster_id", broadcasterID, "error", err)
		return nil
	}
	if err := ep.sentiment.PublishSentiment(ctx, broadcasterID, snapshot); err != nil {
		slog.WarnContext(ctx, "Failed to push config update to overlay", "broadcaster_id", broadcasterID, "error", err)
	}

	return nil
}
