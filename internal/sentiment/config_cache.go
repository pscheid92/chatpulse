package sentiment

import (
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

// ConfigCache provides in-memory caching of session configs with TTL-based expiration.
// This dramatically reduces Redis load by caching frequently-accessed configs that rarely change.
//
// Performance impact:
// - Without cache: 20 reads/sec per session (50ms tick rate)
// - With cache (10s TTL): ~0.1 reads/sec per session (when TTL expires)
// - 1,000 sessions: 20,000 → 100 reads/sec (99.5% reduction)
type ConfigCache struct {
	mu      sync.RWMutex
	entries map[uuid.UUID]*cacheEntry
	ttl     time.Duration
	clock   clockwork.Clock
}

type cacheEntry struct {
	config    domain.ConfigSnapshot
	expiresAt time.Time
}

// NewConfigCache creates a new config cache with the specified TTL.
// Recommended TTL: 10 seconds (balances freshness vs. cache efficiency)
func NewConfigCache(ttl time.Duration, clock clockwork.Clock) *ConfigCache {
	return &ConfigCache{
		entries: make(map[uuid.UUID]*cacheEntry),
		ttl:     ttl,
		clock:   clock,
	}
}

// Get retrieves a cached config if present and not expired.
// Returns (config, true) on cache hit, (nil, false) on cache miss or expiry.
func (c *ConfigCache) Get(sessionUUID uuid.UUID) (*domain.ConfigSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[sessionUUID]
	if !ok {
		return nil, false
	}

	// Check if entry has expired
	if c.clock.Now().After(entry.expiresAt) {
		// Expired entry, treat as cache miss
		// Note: We don't delete here (read lock only). Eviction happens periodically.
		return nil, false
	}

	return &entry.config, true
}

// Set stores a config in the cache with current timestamp + TTL.
func (c *ConfigCache) Set(sessionUUID uuid.UUID, config domain.ConfigSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[sessionUUID] = &cacheEntry{
		config:    config,
		expiresAt: c.clock.Now().Add(c.ttl),
	}
}

// Invalidate explicitly removes a config from the cache.
// Used when config is updated in the database to force immediate refresh.
func (c *ConfigCache) Invalidate(sessionUUID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, sessionUUID)
}

// Clear removes all entries from the cache.
// Useful for testing or manual cache flush.
func (c *ConfigCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[uuid.UUID]*cacheEntry)
}

// Size returns the current number of entries in the cache (including expired).
func (c *ConfigCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// EvictExpired removes all expired entries from the cache and returns the count evicted.
// This prevents unbounded cache growth over time.
func (c *ConfigCache) EvictExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now()
	evicted := 0

	for uuid, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, uuid)
			evicted++
		}
	}

	return evicted
}

// StartEvictionTimer starts a background goroutine that periodically evicts expired entries.
// Returns a stop function that should be called to clean up the goroutine.
//
// Recommended interval: 1 minute (balances cleanup frequency vs. overhead)
func (c *ConfigCache) StartEvictionTimer(interval time.Duration) func() {
	ticker := c.clock.NewTicker(interval)
	done := make(chan bool)

	go func() {
		for {
			select {
			case <-ticker.Chan():
				evicted := c.EvictExpired()
				if evicted > 0 {
					slog.Debug("Evicted expired config cache entries",
						"count", evicted,
						"remaining", c.Size(),
					)
					metrics.ConfigCacheEvictions.Add(float64(evicted))
				}
				// Update cache size metric
				metrics.ConfigCacheSize.Set(float64(c.Size()))

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
