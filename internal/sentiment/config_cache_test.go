package sentiment

import (
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigCache_CacheMiss(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	// Get non-existent key should return miss
	config, hit := cache.Get("broadcaster-miss")
	assert.False(t, hit, "Should be cache miss for non-existent key")
	assert.Nil(t, config, "Config should be nil on miss")
}

func TestConfigCache_CacheHit(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	broadcasterID := "broadcaster-hit"
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.5,
	}

	// Set value
	cache.Set(broadcasterID, testConfig)

	// Get should return cached value
	config, hit := cache.Get(broadcasterID)
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

	broadcasterID := "broadcaster-ttl"
	testConfig := domain.ConfigSnapshot{ForTrigger: "test"}

	// Set value
	cache.Set(broadcasterID, testConfig)

	// Immediately after set, should hit
	_, hit := cache.Get(broadcasterID)
	assert.True(t, hit, "Should hit immediately after set")

	// Advance time by 9 seconds (still within TTL)
	clock.Advance(9 * time.Second)
	_, hit = cache.Get(broadcasterID)
	assert.True(t, hit, "Should still hit at 9 seconds")

	// Advance time by 2 more seconds (total 11s, past TTL)
	clock.Advance(2 * time.Second)
	_, hit = cache.Get(broadcasterID)
	assert.False(t, hit, "Should miss after TTL expires")
}

func TestConfigCache_ExplicitInvalidation(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	broadcasterID := "broadcaster-invalidate"
	testConfig := domain.ConfigSnapshot{ForTrigger: "test"}

	// Set value
	cache.Set(broadcasterID, testConfig)

	// Should hit
	_, hit := cache.Get(broadcasterID)
	assert.True(t, hit)

	// Invalidate
	cache.Invalidate(broadcasterID)

	// Should miss after invalidation
	_, hit = cache.Get(broadcasterID)
	assert.False(t, hit, "Should miss after explicit invalidation")
}

func TestConfigCache_Clear(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	// Set multiple values
	for i := 0; i < 5; i++ {
		cache.Set(fmt.Sprintf("broadcaster-%d", i), domain.ConfigSnapshot{ForTrigger: "test"})
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
	cache.Set("broadcaster-1", domain.ConfigSnapshot{ForTrigger: "test1"})
	clock.Advance(5 * time.Second)
	cache.Set("broadcaster-2", domain.ConfigSnapshot{ForTrigger: "test2"})
	clock.Advance(5 * time.Second)
	cache.Set("broadcaster-3", domain.ConfigSnapshot{ForTrigger: "test3"})

	// At this point with 10s TTL:
	// - broadcaster-1: set at t=0s, expires at t=10s
	// - broadcaster-2: set at t=5s, expires at t=15s
	// - broadcaster-3: set at t=10s, expires at t=20s
	// Current time: 10s from start

	assert.Equal(t, 3, cache.Size())

	// Advance to 11s (broadcaster-1 has expired)
	clock.Advance(1 * time.Second)

	evicted := cache.EvictExpired()
	assert.Equal(t, 1, evicted, "Should evict 1 expired entry")
	assert.Equal(t, 2, cache.Size(), "Should have 2 remaining")

	// broadcaster-2 and broadcaster-3 should still be available
	_, hit2 := cache.Get("broadcaster-2")
	_, hit3 := cache.Get("broadcaster-3")
	assert.True(t, hit2, "broadcaster-2 should still be cached")
	assert.True(t, hit3, "broadcaster-3 should still be cached")

	// Advance to 16s (broadcaster-2 has now expired, broadcaster-3 still valid until 20s)
	clock.Advance(5 * time.Second)

	evicted = cache.EvictExpired()
	assert.Equal(t, 1, evicted, "Should evict 1 more entry")
	assert.Equal(t, 1, cache.Size(), "Should have 1 remaining")

	// Only broadcaster-3 should remain
	_, hit3 = cache.Get("broadcaster-3")
	assert.True(t, hit3, "broadcaster-3 should still be cached")
}

func TestConfigCache_Size(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)

	assert.Equal(t, 0, cache.Size(), "New cache should be empty")

	// Add entries
	for i := 0; i < 10; i++ {
		cache.Set(fmt.Sprintf("broadcaster-%d", i), domain.ConfigSnapshot{ForTrigger: "test"})
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

	broadcasterID := "broadcaster-update"

	// Set initial value
	cache.Set(broadcasterID, domain.ConfigSnapshot{ForTrigger: "initial"})

	config, hit := cache.Get(broadcasterID)
	require.True(t, hit)
	assert.Equal(t, "initial", config.ForTrigger)

	// Update with new value
	cache.Set(broadcasterID, domain.ConfigSnapshot{ForTrigger: "updated"})

	config, hit = cache.Get(broadcasterID)
	require.True(t, hit)
	assert.Equal(t, "updated", config.ForTrigger, "Should return updated value")
}

func TestConfigCache_ConcurrentAccess(t *testing.T) {
	// This test verifies thread safety with -race flag
	clock := clockwork.NewRealClock() // Use real clock for concurrency test
	cache := NewConfigCache(10*time.Second, clock)

	broadcasterID := "broadcaster-concurrent"
	testConfig := domain.ConfigSnapshot{ForTrigger: "test"}

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Set(broadcasterID, testConfig)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Get(broadcasterID)
		}
		done <- true
	}()

	// Invalidator goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Invalidate(broadcasterID)
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
		cache.Set(fmt.Sprintf("broadcaster-%d", i), domain.ConfigSnapshot{ForTrigger: "test"})
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

	// Create multiple broadcaster configs
	broadcasters := make(map[string]domain.ConfigSnapshot)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("broadcaster-%d", i)
		config := domain.ConfigSnapshot{
			ForTrigger: id, // Use ID as unique identifier
		}
		broadcasters[id] = config
		cache.Set(id, config)
	}

	// Verify all are cached correctly
	for id, expectedConfig := range broadcasters {
		config, hit := cache.Get(id)
		require.True(t, hit, "Should hit for broadcaster %s", id)
		assert.Equal(t, expectedConfig.ForTrigger, config.ForTrigger)
	}

	assert.Equal(t, 100, cache.Size())
}

func TestConfigCache_ZeroTTL(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(0, clock) // Zero TTL means immediate expiry

	broadcasterID := "broadcaster-zero-ttl"
	cache.Set(broadcasterID, domain.ConfigSnapshot{ForTrigger: "test"})

	// Even with zero TTL, should work if checked immediately (same instant)
	// But this is an edge case - production should use positive TTL
	config, hit := cache.Get(broadcasterID)
	if hit {
		assert.NotNil(t, config)
	}

	// After any time advancement, should expire
	clock.Advance(1 * time.Nanosecond)
	_, hit = cache.Get(broadcasterID)
	assert.False(t, hit, "Should expire immediately with zero TTL")
}
