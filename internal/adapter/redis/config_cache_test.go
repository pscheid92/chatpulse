package redis

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConfigRepository implements domain.ConfigRepository for tests.
type mockConfigRepository struct {
	getByBroadcasterIDFn func(ctx context.Context, broadcasterID string) (*domain.OverlayConfigWithVersion, error)
}

func (m *mockConfigRepository) GetByStreamerID(_ context.Context, _ uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
	return nil, domain.ErrConfigNotFound
}

func (m *mockConfigRepository) GetByBroadcasterID(ctx context.Context, broadcasterID string) (*domain.OverlayConfigWithVersion, error) {
	if m.getByBroadcasterIDFn != nil {
		return m.getByBroadcasterIDFn(ctx, broadcasterID)
	}
	return nil, domain.ErrConfigNotFound
}

func (m *mockConfigRepository) Update(_ context.Context, _ uuid.UUID, _ domain.OverlayConfig, _ int) error {
	return nil
}

// --- In-memory cache unit tests (no Redis needed) ---

func TestMemoryCache_Miss(t *testing.T) {
	cache := newMemoryCache(10 * time.Second)

	_, hit := cache.get("broadcaster-miss")
	assert.False(t, hit, "Should be cache miss for non-existent key")
}

func TestMemoryCache_Hit(t *testing.T) {
	cache := newMemoryCache(10 * time.Second)

	broadcasterID := "broadcaster-hit"
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		ForLabel:       "For",
		AgainstTrigger: "no",
		AgainstLabel:   "Against",
	}

	cache.set(broadcasterID, testConfig)

	config, hit := cache.get(broadcasterID)
	require.True(t, hit, "Should be cache hit")
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.AgainstLabel)
	assert.Equal(t, "For", config.ForLabel)
	assert.Equal(t, 30, config.MemorySeconds)
}

func TestMemoryCache_TTLExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newMemoryCache(10 * time.Second)

		broadcasterID := "broadcaster-ttl"
		testConfig := domain.OverlayConfig{ForTrigger: "test"}

		cache.set(broadcasterID, testConfig)

		_, hit := cache.get(broadcasterID)
		assert.True(t, hit, "Should hit immediately after set")

		time.Sleep(9 * time.Second)
		_, hit = cache.get(broadcasterID)
		assert.True(t, hit, "Should still hit at 9 seconds")

		time.Sleep(2 * time.Second)
		_, hit = cache.get(broadcasterID)
		assert.False(t, hit, "Should miss after TTL expires")
	})
}

func TestMemoryCache_ExplicitInvalidation(t *testing.T) {
	cache := newMemoryCache(10 * time.Second)

	broadcasterID := "broadcaster-invalidate"
	testConfig := domain.OverlayConfig{ForTrigger: "test"}

	cache.set(broadcasterID, testConfig)

	_, hit := cache.get(broadcasterID)
	assert.True(t, hit)

	cache.invalidate(broadcasterID)

	_, hit = cache.get(broadcasterID)
	assert.False(t, hit, "Should miss after explicit invalidation")
}

func TestMemoryCache_EvictExpired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newMemoryCache(10 * time.Second)

		cache.set("broadcaster-1", domain.OverlayConfig{ForTrigger: "test1"})
		time.Sleep(5 * time.Second)
		cache.set("broadcaster-2", domain.OverlayConfig{ForTrigger: "test2"})
		time.Sleep(5 * time.Second)
		cache.set("broadcaster-3", domain.OverlayConfig{ForTrigger: "test3"})

		assert.Equal(t, 3, cache.size())

		time.Sleep(1 * time.Second)

		evicted := cache.evictExpired()
		assert.Equal(t, 1, evicted, "Should evict 1 expired entry")
		assert.Equal(t, 2, cache.size(), "Should have 2 remaining")

		_, hit2 := cache.get("broadcaster-2")
		_, hit3 := cache.get("broadcaster-3")
		assert.True(t, hit2, "broadcaster-2 should still be cached")
		assert.True(t, hit3, "broadcaster-3 should still be cached")

		time.Sleep(5 * time.Second)

		evicted = cache.evictExpired()
		assert.Equal(t, 1, evicted, "Should evict 1 more entry")
		assert.Equal(t, 1, cache.size(), "Should have 1 remaining")

		_, hit3 = cache.get("broadcaster-3")
		assert.True(t, hit3, "broadcaster-3 should still be cached")
	})
}

func TestMemoryCache_Size(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newMemoryCache(10 * time.Second)

		assert.Equal(t, 0, cache.size(), "New cache should be empty")

		for i := range 10 {
			cache.set(fmt.Sprintf("broadcaster-%d", i), domain.OverlayConfig{ForTrigger: "test"})
		}

		assert.Equal(t, 10, cache.size(), "Should have 10 entries")

		time.Sleep(11 * time.Second)
		assert.Equal(t, 10, cache.size(), "Size includes expired entries")

		cache.evictExpired()
		assert.Equal(t, 0, cache.size(), "All expired entries evicted")
	})
}

func TestMemoryCache_UpdateExisting(t *testing.T) {
	cache := newMemoryCache(10 * time.Second)

	broadcasterID := "broadcaster-update"

	cache.set(broadcasterID, domain.OverlayConfig{ForTrigger: "initial"})

	config, hit := cache.get(broadcasterID)
	require.True(t, hit)
	assert.Equal(t, "initial", config.ForTrigger)

	cache.set(broadcasterID, domain.OverlayConfig{ForTrigger: "updated"})

	config, hit = cache.get(broadcasterID)
	require.True(t, hit)
	assert.Equal(t, "updated", config.ForTrigger, "Should return updated value")
}

func TestMemoryCache_ConcurrentAccess(t *testing.T) {
	cache := newMemoryCache(10 * time.Second)

	broadcasterID := "broadcaster-concurrent"
	testConfig := domain.OverlayConfig{ForTrigger: "test"}

	done := make(chan bool)

	go func() {
		for range 100 {
			cache.set(broadcasterID, testConfig)
		}
		done <- true
	}()

	go func() {
		for range 100 {
			cache.get(broadcasterID)
		}
		done <- true
	}()

	go func() {
		for range 100 {
			cache.invalidate(broadcasterID)
		}
		done <- true
	}()

	<-done
	<-done
	<-done
}

func TestMemoryCache_MultipleKeys(t *testing.T) {
	cache := newMemoryCache(10 * time.Second)

	broadcasters := make(map[string]domain.OverlayConfig)
	for i := range 100 {
		id := fmt.Sprintf("broadcaster-%d", i)
		config := domain.OverlayConfig{ForTrigger: id}
		broadcasters[id] = config
		cache.set(id, config)
	}

	for id, expectedConfig := range broadcasters {
		config, hit := cache.get(id)
		require.True(t, hit, "Should hit for broadcaster %s", id)
		assert.Equal(t, expectedConfig.ForTrigger, config.ForTrigger)
	}

	assert.Equal(t, 100, cache.size())
}

func TestMemoryCache_ZeroTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newMemoryCache(0)

		broadcasterID := "broadcaster-zero-ttl"
		cache.set(broadcasterID, domain.OverlayConfig{ForTrigger: "test"})

		_, hit := cache.get(broadcasterID)
		_ = hit

		time.Sleep(1 * time.Nanosecond)
		_, hit = cache.get(broadcasterID)
		assert.False(t, hit, "Should expire immediately with zero TTL")
	})
}

// --- Integration tests (require Redis via testcontainers) ---

func TestConfigCacheRepo_GetConfigByBroadcaster_3LayerCache(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	client := setupTestClient(t)

	callCount := 0
	configRepo := &mockConfigRepository{
		getByBroadcasterIDFn: func(_ context.Context, _ string) (*domain.OverlayConfigWithVersion, error) {
			callCount++
			return &domain.OverlayConfigWithVersion{
				OverlayConfig: domain.OverlayConfig{
					ForTrigger:     "yes",
					AgainstTrigger: "no",
					MemorySeconds:  30,
				},
			}, nil
		},
	}

	repo := NewConfigCacheRepo(client, configRepo, 10*time.Second)

	// First call: miss on all layers, hits PostgreSQL
	config, err := repo.GetConfigByBroadcaster(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, 1, callCount, "Should have called PostgreSQL once")

	// Second call: hits in-memory cache (L1)
	config, err = repo.GetConfigByBroadcaster(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, 1, callCount, "Should still have called PostgreSQL only once (L1 hit)")

	// Invalidate in-memory only
	repo.mem.invalidate("broadcaster-1")

	// Third call: misses L1, hits Redis (L2)
	config, err = repo.GetConfigByBroadcaster(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, 1, callCount, "Should still have called PostgreSQL only once (L2 hit)")

	// Invalidate both layers
	err = repo.InvalidateCache(ctx, "broadcaster-1")
	require.NoError(t, err)

	// Fourth call: misses both caches, hits PostgreSQL again
	config, err = repo.GetConfigByBroadcaster(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, 2, callCount, "Should have called PostgreSQL twice after full invalidation")
}

func TestConfigCacheRepo_InvalidateCache_BothLayers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	client := setupTestClient(t)

	configRepo := &mockConfigRepository{
		getByBroadcasterIDFn: func(_ context.Context, _ string) (*domain.OverlayConfigWithVersion, error) {
			return &domain.OverlayConfigWithVersion{
				OverlayConfig: domain.OverlayConfig{ForTrigger: "yes"},
			}, nil
		},
	}

	repo := NewConfigCacheRepo(client, configRepo, 10*time.Second)

	// Populate both caches
	_, err := repo.GetConfigByBroadcaster(ctx, "broadcaster-1")
	require.NoError(t, err)

	// Verify in-memory has it
	_, hit := repo.mem.get("broadcaster-1")
	assert.True(t, hit, "In-memory cache should have entry")

	// Verify Redis has it
	_, redisHit := repo.getCached(ctx, "broadcaster-1")
	assert.True(t, redisHit, "Redis cache should have entry")

	// Invalidate
	err = repo.InvalidateCache(ctx, "broadcaster-1")
	require.NoError(t, err)

	// Both should be empty
	_, hit = repo.mem.get("broadcaster-1")
	assert.False(t, hit, "In-memory cache should be empty after invalidation")

	_, redisHit = repo.getCached(ctx, "broadcaster-1")
	assert.False(t, redisHit, "Redis cache should be empty after invalidation")
}

func TestConfigCacheRepo_EvictionTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		configRepo := &mockConfigRepository{}
		// Use a nil-ish Redis client since we only test in-memory eviction here
		cache := newMemoryCache(5 * time.Second)

		for i := range 5 {
			cache.set(fmt.Sprintf("broadcaster-%d", i), domain.OverlayConfig{ForTrigger: "test"})
		}

		assert.Equal(t, 5, cache.size())

		// Manually test eviction (StartEvictionTimer delegates to memoryCache)
		repo := &ConfigCacheRepo{mem: cache, configs: configRepo}
		stopEviction := repo.StartEvictionTimer(1 * time.Second)
		defer stopEviction()

		time.Sleep(6 * time.Second)
		time.Sleep(1 * time.Second)
		synctest.Wait()

		assert.Equal(t, 0, cache.size(), "Eviction timer should have cleaned up expired entries")
	})
}
