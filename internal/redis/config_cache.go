package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

const (
	configCachePrefix = "config_cache:"
	configCacheTTL    = 1 * time.Hour
)

// ConfigCacheRepo provides read-through config caching: Redis → PostgreSQL.
// The in-memory cache (sentiment.ConfigCache) sits above this layer.
type ConfigCacheRepo struct {
	rdb     goredis.Cmdable
	configs domain.ConfigRepository
}

// NewConfigCacheRepo creates a new read-through config cache.
func NewConfigCacheRepo(rdb goredis.Cmdable, configs domain.ConfigRepository) *ConfigCacheRepo {
	return &ConfigCacheRepo{rdb: rdb, configs: configs}
}

// GetConfigByBroadcaster looks up config by broadcaster ID with read-through caching.
// Read path: Redis GET → PostgreSQL query → populate Redis cache.
func (r *ConfigCacheRepo) GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (*domain.ConfigSnapshot, error) {
	key := configCachePrefix + broadcasterID

	// Try Redis cache
	data, err := r.rdb.Get(ctx, key).Bytes()
	if err == nil {
		var snapshot domain.ConfigSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			slog.Warn("Failed to unmarshal cached config, falling through to PostgreSQL",
				"broadcaster_id", broadcasterID, "error", err)
		} else {
			metrics.ConfigCacheRedisHits.Inc()
			return &snapshot, nil
		}
	} else if !errors.Is(err, goredis.Nil) {
		// Redis error — log and fall through to PostgreSQL
		slog.Warn("Redis config cache GET failed, falling through to PostgreSQL",
			"broadcaster_id", broadcasterID, "error", err)
	}

	// Redis miss or error — query PostgreSQL
	config, err := r.configs.GetByBroadcasterID(ctx, broadcasterID)
	if err != nil {
		return nil, fmt.Errorf("config lookup by broadcaster_id failed: %w", err)
	}

	snapshot := &domain.ConfigSnapshot{
		ForTrigger:     config.ForTrigger,
		AgainstTrigger: config.AgainstTrigger,
		LeftLabel:      config.LeftLabel,
		RightLabel:     config.RightLabel,
		DecaySpeed:     config.DecaySpeed,
		Version:        config.Version,
	}

	metrics.ConfigCachePostgresHits.Inc()

	// Populate Redis cache (best-effort)
	if encoded, err := json.Marshal(snapshot); err == nil {
		if err := r.rdb.Set(ctx, key, encoded, configCacheTTL).Err(); err != nil {
			slog.Warn("Failed to populate Redis config cache",
				"broadcaster_id", broadcasterID, "error", err)
		}
	}

	return snapshot, nil
}

// InvalidateCache removes a broadcaster's config from the Redis cache.
func (r *ConfigCacheRepo) InvalidateCache(ctx context.Context, broadcasterID string) error {
	if err := r.rdb.Del(ctx, configCachePrefix+broadcasterID).Err(); err != nil {
		return fmt.Errorf("failed to invalidate config cache: %w", err)
	}
	return nil
}
