package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/pscheid92/chatpulse/internal/domain"
)

const configCacheTTL = 1 * time.Hour

type ConfigCacheRepo struct {
	rdb     goredis.Cmdable
	configs domain.ConfigRepository
	mem     *memoryCache
}

func NewConfigCacheRepo(rdb goredis.Cmdable, configs domain.ConfigRepository, memCacheTTL time.Duration) *ConfigCacheRepo {
	return &ConfigCacheRepo{
		rdb:     rdb,
		configs: configs,
		mem:     newMemoryCache(memCacheTTL),
	}
}

// StartEvictionTimer runs a periodic goroutine that evicts expired in-memory cache entries.
// Returns a stop function that should be deferred.
func (r *ConfigCacheRepo) StartEvictionTimer(interval time.Duration) func() {
	ticker := time.NewTicker(interval)
	done := make(chan bool)

	go func() {
		for {
			select {
			case <-ticker.C:
				evicted := r.mem.evictExpired()
				if evicted > 0 {
					slog.Debug("Evicted expired config cache entries", "count", evicted, "remaining", r.mem.size())
				}

			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	return func() {
		close(done)
	}
}

func (r *ConfigCacheRepo) GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (domain.OverlayConfig, error) {
	// Layer 1: in-memory cache
	if config, ok := r.mem.get(broadcasterID); ok {
		return config, nil
	}

	// Layer 2: Redis cache
	if config, ok := r.getCached(ctx, broadcasterID); ok {
		r.mem.set(broadcasterID, config)
		return config, nil
	}

	// Layer 3: PostgreSQL
	result, err := r.configs.GetByBroadcasterID(ctx, broadcasterID)
	if err != nil {
		return domain.OverlayConfig{}, fmt.Errorf("config lookup by broadcaster_id failed: %w", err)
	}

	r.mem.set(broadcasterID, result.OverlayConfig)
	r.writeCache(ctx, broadcasterID, result.OverlayConfig)
	return result.OverlayConfig, nil
}

// InvalidateCache evicts the config from both the in-memory cache and Redis cache.
func (r *ConfigCacheRepo) InvalidateCache(ctx context.Context, broadcasterID string) error {
	r.mem.invalidate(broadcasterID)

	ck := configCacheKey(broadcasterID)
	if err := r.rdb.Del(ctx, ck).Err(); err != nil {
		return fmt.Errorf("failed to invalidate config cache: %w", err)
	}
	return nil
}

func (r *ConfigCacheRepo) writeCache(ctx context.Context, broadcasterID string, config domain.OverlayConfig) {
	key := configCacheKey(broadcasterID)

	encoded, err := json.Marshal(config)
	if err != nil {
		slog.Warn("Failed to marshal config for Redis cache", "broadcaster_id", broadcasterID, "error", err)
		return
	}

	err = r.rdb.Set(ctx, key, encoded, configCacheTTL).Err()
	if err != nil {
		slog.Warn("Failed to populate Redis config cache", "broadcaster_id", broadcasterID, "error", err)
	}
}

func (r *ConfigCacheRepo) getCached(ctx context.Context, broadcasterID string) (domain.OverlayConfig, bool) {
	ck := configCacheKey(broadcasterID)

	data, err := r.rdb.Get(ctx, ck).Bytes()
	if err != nil {
		if !errors.Is(err, goredis.Nil) {
			slog.Warn("Redis config cache GET failed", "broadcaster_id", broadcasterID, "error", err)
		}
		return domain.OverlayConfig{}, false
	}

	var config domain.OverlayConfig
	if err := json.Unmarshal(data, &config); err != nil {
		slog.Warn("Failed to unmarshal cached config", "broadcaster_id", broadcasterID, "error", err)
		return domain.OverlayConfig{}, false
	}

	return config, true
}

func configCacheKey(broadcasterID string) string {
	return "config_cache:" + broadcasterID
}

// memoryCache is an in-memory L1 cache with TTL-based expiry.
type memoryCache struct {
	mu      sync.RWMutex
	entries map[string]*memoryCacheEntry
	ttl     time.Duration
}

type memoryCacheEntry struct {
	config    domain.OverlayConfig
	expiresAt time.Time
}

func newMemoryCache(ttl time.Duration) *memoryCache {
	return &memoryCache{
		entries: make(map[string]*memoryCacheEntry),
		ttl:     ttl,
	}
}

func (c *memoryCache) get(broadcasterID string) (domain.OverlayConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[broadcasterID]
	if !ok {
		return domain.OverlayConfig{}, false
	}

	if time.Now().After(entry.expiresAt) {
		return domain.OverlayConfig{}, false
	}

	return entry.config, true
}

func (c *memoryCache) set(broadcasterID string, config domain.OverlayConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[broadcasterID] = &memoryCacheEntry{
		config:    config,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *memoryCache) invalidate(broadcasterID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, broadcasterID)
}

func (c *memoryCache) size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *memoryCache) evictExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	evicted := 0

	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
			evicted++
		}
	}

	return evicted
}
