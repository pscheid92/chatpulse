package sentiment

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigCache_CacheMiss(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	sessionUUID := uuid.New()

	// Get non-existent key should return miss
	config, hit := cache.Get(sessionUUID)
	assert.False(t, hit, "Should be cache miss for non-existent key")
	assert.Nil(t, config, "Config should be nil on miss")
}

func TestConfigCache_CacheHit(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.5,
	}

	// Set value
	cache.Set(sessionUUID, testConfig)

	// Get should return cached value
	config, hit := cache.Get(sessionUUID)
	require.True(t, hit, "Should be cache hit")
	require.NotNil(t, config)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.LeftLabel)
	assert.Equal(t, "For", config.RightLabel)
	assert.Equal(t, 1.5, config.DecaySpeed)
}

func TestConfigCache_TTLExpiry(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{ForTrigger: "test"}

	// Set value
	cache.Set(sessionUUID, testConfig)

	// Immediately after set, should hit
	_, hit := cache.Get(sessionUUID)
	assert.True(t, hit, "Should hit immediately after set")

	// Advance time by 9 seconds (still within TTL)
	clock.Advance(9 * time.Second)
	_, hit = cache.Get(sessionUUID)
	assert.True(t, hit, "Should still hit at 9 seconds")

	// Advance time by 2 more seconds (total 11s, past TTL)
	clock.Advance(2 * time.Second)
	_, hit = cache.Get(sessionUUID)
	assert.False(t, hit, "Should miss after TTL expires")
}

func TestConfigCache_ExplicitInvalidation(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{ForTrigger: "test"}

	// Set value
	cache.Set(sessionUUID, testConfig)

	// Should hit
	_, hit := cache.Get(sessionUUID)
	assert.True(t, hit)

	// Invalidate
	cache.Invalidate(sessionUUID)

	// Should miss after invalidation
	_, hit = cache.Get(sessionUUID)
	assert.False(t, hit, "Should miss after explicit invalidation")
}

func TestConfigCache_Clear(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	// Set multiple values
	for i := 0; i < 5; i++ {
		cache.Set(uuid.New(), domain.ConfigSnapshot{ForTrigger: "test"})
	}

	assert.Equal(t, 5, cache.Size(), "Should have 5 entries")

	// Clear all
	cache.Clear()

	assert.Equal(t, 0, cache.Size(), "Should have 0 entries after clear")
}

func TestConfigCache_EvictExpired(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	// Set 3 entries
	uuid1 := uuid.New()
	uuid2 := uuid.New()
	uuid3 := uuid.New()

	cache.Set(uuid1, domain.ConfigSnapshot{ForTrigger: "test1"})
	clock.Advance(5 * time.Second)
	cache.Set(uuid2, domain.ConfigSnapshot{ForTrigger: "test2"})
	clock.Advance(5 * time.Second)
	cache.Set(uuid3, domain.ConfigSnapshot{ForTrigger: "test3"})

	// At this point with 10s TTL:
	// - uuid1: set at t=0s, expires at t=10s
	// - uuid2: set at t=5s, expires at t=15s
	// - uuid3: set at t=10s, expires at t=20s
	// Current time: 10s from start

	assert.Equal(t, 3, cache.Size())

	// Advance to 11s (uuid1 has expired)
	clock.Advance(1 * time.Second)

	evicted := cache.EvictExpired()
	assert.Equal(t, 1, evicted, "Should evict 1 expired entry")
	assert.Equal(t, 2, cache.Size(), "Should have 2 remaining")

	// uuid2 and uuid3 should still be available
	_, hit2 := cache.Get(uuid2)
	_, hit3 := cache.Get(uuid3)
	assert.True(t, hit2, "uuid2 should still be cached")
	assert.True(t, hit3, "uuid3 should still be cached")

	// Advance to 16s (uuid2 has now expired, uuid3 still valid until 20s)
	clock.Advance(5 * time.Second)

	evicted = cache.EvictExpired()
	assert.Equal(t, 1, evicted, "Should evict 1 more entry")
	assert.Equal(t, 1, cache.Size(), "Should have 1 remaining")

	// Only uuid3 should remain
	_, hit3 = cache.Get(uuid3)
	assert.True(t, hit3, "uuid3 should still be cached")
}

func TestConfigCache_Size(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	assert.Equal(t, 0, cache.Size(), "New cache should be empty")

	// Add entries
	for i := 0; i < 10; i++ {
		cache.Set(uuid.New(), domain.ConfigSnapshot{ForTrigger: "test"})
	}

	assert.Equal(t, 10, cache.Size(), "Should have 10 entries")

	// Size includes expired entries until eviction
	clock.Advance(11 * time.Second)
	assert.Equal(t, 10, cache.Size(), "Size includes expired entries")

	// After eviction, size should reflect actual entries
	cache.EvictExpired()
	assert.Equal(t, 0, cache.Size(), "All expired entries evicted")
}

func TestConfigCache_UpdateExisting(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	sessionUUID := uuid.New()

	// Set initial value
	cache.Set(sessionUUID, domain.ConfigSnapshot{ForTrigger: "initial"})

	config, hit := cache.Get(sessionUUID)
	require.True(t, hit)
	assert.Equal(t, "initial", config.ForTrigger)

	// Update with new value
	cache.Set(sessionUUID, domain.ConfigSnapshot{ForTrigger: "updated"})

	config, hit = cache.Get(sessionUUID)
	require.True(t, hit)
	assert.Equal(t, "updated", config.ForTrigger, "Should return updated value")
}

func TestConfigCache_ConcurrentAccess(t *testing.T) {
	// This test verifies thread safety with -race flag
	clock := clockwork.NewRealClock() // Use real clock for concurrency test
	cache := NewConfigCache(10*time.Second, clock)

	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{ForTrigger: "test"}

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Set(sessionUUID, testConfig)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Get(sessionUUID)
		}
		done <- true
	}()

	// Invalidator goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Invalidate(sessionUUID)
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done

	// If we got here without race detector warnings, we're good
	t.Log("Concurrent access test passed")
}

func TestConfigCache_EvictionTimer(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(5*time.Second, clock)

	// Add entries that will expire
	for i := 0; i < 5; i++ {
		cache.Set(uuid.New(), domain.ConfigSnapshot{ForTrigger: "test"})
	}

	assert.Equal(t, 5, cache.Size())

	// Start eviction timer with 1-second interval
	stopEviction := cache.StartEvictionTimer(1 * time.Second)
	defer stopEviction()

	// Advance time past TTL
	clock.Advance(6 * time.Second)

	// Trigger the ticker (advance time by interval)
	clock.Advance(1 * time.Second)

	// Give the goroutine a moment to process
	time.Sleep(50 * time.Millisecond)

	// All entries should be evicted
	assert.Equal(t, 0, cache.Size(), "Eviction timer should have cleaned up expired entries")
}

func TestConfigCache_MultipleKeys(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	// Create multiple session configs
	sessions := make(map[uuid.UUID]domain.ConfigSnapshot)
	for i := 0; i < 100; i++ {
		uuid := uuid.New()
		config := domain.ConfigSnapshot{
			ForTrigger: uuid.String(), // Use UUID as unique identifier
		}
		sessions[uuid] = config
		cache.Set(uuid, config)
	}

	// Verify all are cached correctly
	for uuid, expectedConfig := range sessions {
		config, hit := cache.Get(uuid)
		require.True(t, hit, "Should hit for UUID %s", uuid)
		assert.Equal(t, expectedConfig.ForTrigger, config.ForTrigger)
	}

	assert.Equal(t, 100, cache.Size())
}

func TestConfigCache_ZeroTTL(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(0, clock) // Zero TTL means immediate expiry

	sessionUUID := uuid.New()
	cache.Set(sessionUUID, domain.ConfigSnapshot{ForTrigger: "test"})

	// Even with zero TTL, should work if checked immediately (same instant)
	// But this is an edge case - production should use positive TTL
	config, hit := cache.Get(sessionUUID)
	if hit {
		assert.NotNil(t, config)
	}

	// After any time advancement, should expire
	clock.Advance(1 * time.Nanosecond)
	_, hit = cache.Get(sessionUUID)
	assert.False(t, hit, "Should expire immediately with zero TTL")
}
