package sentiment

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingConfigSource wraps mockConfigSource and tracks GetConfigByBroadcaster calls.
type trackingConfigSource struct {
	*mockConfigSource
	getConfigCalls atomic.Int64
}

func (t *trackingConfigSource) GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (*domain.ConfigSnapshot, error) {
	t.getConfigCalls.Add(1)
	return t.mockConfigSource.GetConfigByBroadcaster(ctx, broadcasterID)
}

// TestConfigCache_Integration_HighHitRate verifies the config cache achieves >99% hit rate
// when the broadcaster repeatedly calls GetBroadcastData for the same broadcaster.
func TestConfigCache_Integration_HighHitRate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	testCfg := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingSource := &trackingConfigSource{
		mockConfigSource: &mockConfigSource{
			getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
				return testCfg, nil
			},
		},
	}

	sentimentStore := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ string, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(trackingSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, cache)

	ctx := context.Background()

	// Simulate broadcaster tick loop calling GetBroadcastData repeatedly
	const numCalls = 10000
	for i := 0; i < numCalls; i++ {
		_, err := engine.GetBroadcastData(ctx, "broadcaster-1")
		require.NoError(t, err)

		// Advance time by 1ms per call (simulates 10s worth of ticks)
		fakeClock.Advance(1 * time.Millisecond)
	}

	// Verify cache hit rate
	configCalls := trackingSource.getConfigCalls.Load()
	hitRate := float64(numCalls-configCalls) / float64(numCalls) * 100.0

	t.Logf("Config cache performance: %d GetBroadcastData calls, %d config source reads, %.2f%% hit rate",
		numCalls, configCalls, hitRate)

	// First call is a miss, then all subsequent calls within TTL are hits
	// With 10s TTL and 1ms per call, we have 10000 calls but only 1 config source read
	assert.Equal(t, int64(1), configCalls, "Should only read from config source once (first call)")
	assert.Greater(t, hitRate, 99.0, "Cache hit rate should exceed 99%")
}

// TestConfigCache_Integration_TTLExpiry verifies the cache correctly expires
// and refreshes config when TTL elapses.
func TestConfigCache_Integration_TTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	testCfg := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingSource := &trackingConfigSource{
		mockConfigSource: &mockConfigSource{
			getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
				return testCfg, nil
			},
		},
	}

	sentimentStore := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ string, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(5*time.Second, fakeClock)
	engine := NewEngine(trackingSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, cache)

	ctx := context.Background()

	// First call - cache miss
	_, err := engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingSource.getConfigCalls.Load(), "First call should miss")

	// Advance time by 4s (still within TTL)
	fakeClock.Advance(4 * time.Second)
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingSource.getConfigCalls.Load(), "Should still be cached")

	// Advance time by 2s (total 6s, past 5s TTL)
	fakeClock.Advance(2 * time.Second)
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingSource.getConfigCalls.Load(), "Should refresh after TTL")

	// Next call should hit cache again
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingSource.getConfigCalls.Load(), "Should be cached again")
}

// TestConfigCache_Integration_Invalidation verifies the cache is invalidated
// when config is updated.
func TestConfigCache_Integration_Invalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	testCfg := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingSource := &trackingConfigSource{
		mockConfigSource: &mockConfigSource{
			getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
				return testCfg, nil
			},
		},
	}

	sentimentStore := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ string, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(trackingSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, cache)

	ctx := context.Background()

	// First call - populate cache
	_, err := engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingSource.getConfigCalls.Load())

	// Second call - should hit cache
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingSource.getConfigCalls.Load(), "Should be cached")

	// Invalidate cache by broadcaster_id (simulates config update)
	cache.Invalidate("broadcaster-1")

	// Next call should miss cache and refetch
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingSource.getConfigCalls.Load(), "Should refetch after invalidation")
}

// TestConfigCache_Integration_MultipleBroadcastersIsolation verifies that cache
// entries for different broadcasters are isolated.
func TestConfigCache_Integration_MultipleBroadcastersIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()

	config1 := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no", DecaySpeed: 1.0}
	config2 := &domain.ConfigSnapshot{ForTrigger: "yay", AgainstTrigger: "nay", DecaySpeed: 0.5}

	trackingSource := &trackingConfigSource{
		mockConfigSource: &mockConfigSource{
			getConfigByBroadcasterFn: func(_ context.Context, broadcasterID string) (*domain.ConfigSnapshot, error) {
				if broadcasterID == "broadcaster-1" {
					return config1, nil
				}
				return config2, nil
			},
		},
	}

	sentimentStore := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ string, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(trackingSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, cache)

	ctx := context.Background()

	// Call broadcaster-1 twice
	_, err := engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)

	// Call broadcaster-2 twice
	_, err = engine.GetBroadcastData(ctx, "broadcaster-2")
	require.NoError(t, err)
	_, err = engine.GetBroadcastData(ctx, "broadcaster-2")
	require.NoError(t, err)

	// Should have 2 config source calls total (one per broadcaster, second calls hit cache)
	assert.Equal(t, int64(2), trackingSource.getConfigCalls.Load(),
		"Should have one config source call per broadcaster")

	// Verify cache has both entries
	assert.Equal(t, 2, cache.Size(), "Cache should have 2 entries")

	// Invalidate broadcaster-1, should not affect broadcaster-2
	cache.Invalidate("broadcaster-1")

	// broadcaster-2 should still hit cache
	_, err = engine.GetBroadcastData(ctx, "broadcaster-2")
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingSource.getConfigCalls.Load(),
		"broadcaster-2 should still be cached after broadcaster-1 invalidation")

	// broadcaster-1 should miss cache
	_, err = engine.GetBroadcastData(ctx, "broadcaster-1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), trackingSource.getConfigCalls.Load(),
		"broadcaster-1 should refetch after invalidation")
}

// TestConfigCache_Integration_ConcurrentAccess verifies cache is thread-safe
// under concurrent load from multiple simulated broadcaster ticks.
func TestConfigCache_Integration_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Use real clock for concurrency test
	realClock := clockwork.NewRealClock()
	testCfg := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingSource := &trackingConfigSource{
		mockConfigSource: &mockConfigSource{
			getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
				// Add tiny delay to increase chance of race conditions
				time.Sleep(100 * time.Microsecond)
				return testCfg, nil
			},
		},
	}

	sentimentStore := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ string, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, realClock)
	engine := NewEngine(trackingSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, realClock, cache)

	ctx := context.Background()

	// Launch 10 goroutines simulating concurrent broadcaster ticks
	const numGoroutines = 10
	const callsPerGoroutine = 100
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < callsPerGoroutine; j++ {
				_, err := engine.GetBroadcastData(ctx, "broadcaster-1")
				if err != nil {
					t.Errorf("GetBroadcastData failed: %v", err)
				}
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify cache effectiveness under concurrent load
	configCalls := trackingSource.getConfigCalls.Load()
	totalCalls := int64(numGoroutines * callsPerGoroutine)
	hitRate := float64(totalCalls-configCalls) / float64(totalCalls) * 100.0

	t.Logf("Concurrent access: %d total calls, %d config source reads, %.2f%% hit rate",
		totalCalls, configCalls, hitRate)

	// With 10s TTL and fast concurrent calls, should have very few misses
	// (first call + potential race window at start)
	assert.LessOrEqual(t, configCalls, int64(10), "Should have minimal config source calls under concurrent load")
	assert.GreaterOrEqual(t, hitRate, 99.0, "Cache hit rate should be at least 99% even under concurrency")
}
