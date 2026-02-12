package sentiment

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingSessionRepo wraps mockSessionRepo and tracks GetSessionConfig calls.
type trackingSessionRepo struct {
	*mockSessionRepo
	getConfigCalls atomic.Int64
}

func (t *trackingSessionRepo) GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
	t.getConfigCalls.Add(1)
	return t.mockSessionRepo.GetSessionConfig(ctx, sessionUUID)
}

// TestConfigCache_Integration_HighHitRate verifies the config cache achieves >99% hit rate
// when the broadcaster repeatedly calls GetCurrentValue for the same session.
func TestConfigCache_Integration_HighHitRate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	testConfig := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Create tracking session repo to count GetSessionConfig calls
	trackingRepo := &trackingSessionRepo{
		mockSessionRepo: &mockSessionRepo{
			getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
				return testConfig, nil
			},
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(trackingRepo, sentiment, &mockDebouncer{}, fakeClock, cache)

	sessionUUID := uuid.New()
	ctx := context.Background()

	// Simulate broadcaster tick loop calling GetCurrentValue repeatedly
	const numCalls = 10000
	for i := 0; i < numCalls; i++ {
		_, err := engine.GetCurrentValue(ctx, sessionUUID)
		require.NoError(t, err)

		// Advance time by 1ms per call (simulates 10s worth of ticks)
		fakeClock.Advance(1 * time.Millisecond)
	}

	// Verify cache hit rate
	configCalls := trackingRepo.getConfigCalls.Load()
	hitRate := float64(numCalls-configCalls) / float64(numCalls) * 100.0

	t.Logf("Config cache performance: %d GetCurrentValue calls, %d Redis reads, %.2f%% hit rate",
		numCalls, configCalls, hitRate)

	// First call is a miss, then all subsequent calls within TTL are hits
	// With 10s TTL and 1ms per call, we have 10000 calls but only 1 Redis read
	assert.Equal(t, int64(1), configCalls, "Should only read from Redis once (first call)")
	assert.Greater(t, hitRate, 99.0, "Cache hit rate should exceed 99%")
}

// TestConfigCache_Integration_TTLExpiry verifies the cache correctly expires
// and refreshes config when TTL elapses.
func TestConfigCache_Integration_TTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	testConfig := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingRepo := &trackingSessionRepo{
		mockSessionRepo: &mockSessionRepo{
			getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
				return testConfig, nil
			},
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(5*time.Second, fakeClock)
	engine := NewEngine(trackingRepo, sentiment, &mockDebouncer{}, fakeClock, cache)

	sessionUUID := uuid.New()
	ctx := context.Background()

	// First call - cache miss
	_, err := engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingRepo.getConfigCalls.Load(), "First call should miss")

	// Advance time by 4s (still within TTL)
	fakeClock.Advance(4 * time.Second)
	_, err = engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingRepo.getConfigCalls.Load(), "Should still be cached")

	// Advance time by 2s (total 6s, past 5s TTL)
	fakeClock.Advance(2 * time.Second)
	_, err = engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingRepo.getConfigCalls.Load(), "Should refresh after TTL")

	// Next call should hit cache again
	_, err = engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingRepo.getConfigCalls.Load(), "Should be cached again")
}

// TestConfigCache_Integration_Invalidation verifies the cache is invalidated
// when config is updated.
func TestConfigCache_Integration_Invalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	testConfig := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingRepo := &trackingSessionRepo{
		mockSessionRepo: &mockSessionRepo{
			getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
				return testConfig, nil
			},
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(trackingRepo, sentiment, &mockDebouncer{}, fakeClock, cache)

	sessionUUID := uuid.New()
	ctx := context.Background()

	// First call - populate cache
	_, err := engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingRepo.getConfigCalls.Load())

	// Second call - should hit cache
	_, err = engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), trackingRepo.getConfigCalls.Load(), "Should be cached")

	// Invalidate cache (simulates config update)
	engine.InvalidateConfigCache(sessionUUID)

	// Next call should miss cache and refetch
	_, err = engine.GetCurrentValue(ctx, sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingRepo.getConfigCalls.Load(), "Should refetch after invalidation")
}

// TestConfigCache_Integration_MultipleSessionsIsolation verifies that cache
// entries for different sessions are isolated.
func TestConfigCache_Integration_MultipleSessionsIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	session1UUID := uuid.New()
	session2UUID := uuid.New()

	config1 := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no", DecaySpeed: 1.0}
	config2 := &domain.ConfigSnapshot{ForTrigger: "yay", AgainstTrigger: "nay", DecaySpeed: 0.5}

	trackingRepo := &trackingSessionRepo{
		mockSessionRepo: &mockSessionRepo{
			getSessionConfigFn: func(_ context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
				if sessionUUID == session1UUID {
					return config1, nil
				}
				return config2, nil
			},
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(trackingRepo, sentiment, &mockDebouncer{}, fakeClock, cache)

	ctx := context.Background()

	// Call session1 twice
	_, err := engine.GetCurrentValue(ctx, session1UUID)
	require.NoError(t, err)
	_, err = engine.GetCurrentValue(ctx, session1UUID)
	require.NoError(t, err)

	// Call session2 twice
	_, err = engine.GetCurrentValue(ctx, session2UUID)
	require.NoError(t, err)
	_, err = engine.GetCurrentValue(ctx, session2UUID)
	require.NoError(t, err)

	// Should have 2 Redis calls total (one per session, second calls hit cache)
	assert.Equal(t, int64(2), trackingRepo.getConfigCalls.Load(),
		"Should have one Redis call per session")

	// Verify cache has both entries
	assert.Equal(t, 2, cache.Size(), "Cache should have 2 entries")

	// Invalidate session1, should not affect session2
	engine.InvalidateConfigCache(session1UUID)

	// session2 should still hit cache
	_, err = engine.GetCurrentValue(ctx, session2UUID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), trackingRepo.getConfigCalls.Load(),
		"session2 should still be cached after session1 invalidation")

	// session1 should miss cache
	_, err = engine.GetCurrentValue(ctx, session1UUID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), trackingRepo.getConfigCalls.Load(),
		"session1 should refetch after invalidation")
}

// TestConfigCache_Integration_ConcurrentAccess verifies cache is thread-safe
// under concurrent load from multiple simulated broadcaster ticks.
func TestConfigCache_Integration_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Use real clock for concurrency test
	realClock := clockwork.NewRealClock()
	testConfig := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	trackingRepo := &trackingSessionRepo{
		mockSessionRepo: &mockSessionRepo{
			getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
				// Add tiny delay to increase chance of race conditions
				time.Sleep(100 * time.Microsecond)
				return testConfig, nil
			},
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, realClock)
	engine := NewEngine(trackingRepo, sentiment, &mockDebouncer{}, realClock, cache)

	sessionUUID := uuid.New()
	ctx := context.Background()

	// Launch 10 goroutines simulating concurrent broadcaster ticks
	const numGoroutines = 10
	const callsPerGoroutine = 100
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < callsPerGoroutine; j++ {
				_, err := engine.GetCurrentValue(ctx, sessionUUID)
				if err != nil {
					t.Errorf("GetCurrentValue failed: %v", err)
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
	configCalls := trackingRepo.getConfigCalls.Load()
	totalCalls := int64(numGoroutines * callsPerGoroutine)
	hitRate := float64(totalCalls-configCalls) / float64(totalCalls) * 100.0

	t.Logf("Concurrent access: %d total calls, %d Redis reads, %.2f%% hit rate",
		totalCalls, configCalls, hitRate)

	// With 10s TTL and fast concurrent calls, should have very few misses
	// (first call + potential race window at start)
	assert.LessOrEqual(t, configCalls, int64(10), "Should have minimal Redis calls under concurrent load")
	assert.GreaterOrEqual(t, hitRate, 99.0, "Cache hit rate should be at least 99% even under concurrency")
}
