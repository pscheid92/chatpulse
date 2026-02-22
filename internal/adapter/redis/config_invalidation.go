package redis

import (
	"context"
	"fmt"
	"log/slog"

	goredis "github.com/redis/go-redis/v9"
)

const configInvalidationChannel = "config:invalidate"

type ConfigInvalidationSubscriber struct {
	rdb   *goredis.Client
	cache *ConfigCacheRepo
}

func NewConfigInvalidationSubscriber(rdb *goredis.Client, cache *ConfigCacheRepo) *ConfigInvalidationSubscriber {
	return &ConfigInvalidationSubscriber{rdb: rdb, cache: cache}
}

func (s *ConfigInvalidationSubscriber) Start(ctx context.Context) {
	pubsub := s.rdb.Subscribe(ctx, configInvalidationChannel)
	defer func() { _ = pubsub.Close() }()

	ch := pubsub.Channel()
	for {
		select {
		case msg := <-ch:
			if msg == nil {
				return
			}
			s.handleInvalidation(ctx, msg.Payload)
		case <-ctx.Done():
			return
		}
	}
}

func (s *ConfigInvalidationSubscriber) handleInvalidation(ctx context.Context, payload string) {
	if payload == "" {
		slog.Warn("Empty config invalidation message")
		return
	}

	if err := s.cache.InvalidateCache(ctx, payload); err != nil {
		slog.Warn("Failed to invalidate config cache via pub/sub", "broadcaster_id", payload, "error", err)
	}

	slog.Debug("Config cache invalidated via pub/sub", "broadcaster_id", payload)
}

func PublishConfigInvalidation(ctx context.Context, rdb *goredis.Client, broadcasterID string) error {
	if err := rdb.Publish(ctx, configInvalidationChannel, broadcasterID).Err(); err != nil {
		return fmt.Errorf("failed to publish config invalidation: %w", err)
	}
	return nil
}
