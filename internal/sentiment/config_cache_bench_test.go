package sentiment

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// BenchmarkConfigCache_Get benchmarks cache read performance.
func BenchmarkConfigCache_Get(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Pre-populate cache
	cache.Set(sessionUUID, testConfig)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(sessionUUID)
	}
}

// BenchmarkConfigCache_Set benchmarks cache write performance.
func BenchmarkConfigCache_Set(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Set(sessionUUID, testConfig)
	}
}

// BenchmarkEngine_GetCurrentValue_CacheHit benchmarks GetCurrentValue with cache hit.
func BenchmarkEngine_GetCurrentValue_CacheHit(b *testing.B) {
	fakeClock := clockwork.NewFakeClock()
	testConfig := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	sessions := &mockSessionRepo{
		getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
			return testConfig, nil
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(sessions, sentiment, &mockDebouncer{}, fakeClock, cache)

	sessionUUID := uuid.New()
	ctx := context.Background()

	// Warm up cache with one call
	_, _ = engine.GetCurrentValue(ctx, sessionUUID)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.GetCurrentValue(ctx, sessionUUID)
	}
}

// BenchmarkEngine_GetCurrentValue_CacheMiss benchmarks GetCurrentValue with cache miss.
func BenchmarkEngine_GetCurrentValue_CacheMiss(b *testing.B) {
	fakeClock := clockwork.NewFakeClock()
	testConfig := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	sessions := &mockSessionRepo{
		getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
			return testConfig, nil
		},
	}

	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return 50.0, nil
		},
	}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(sessions, sentiment, &mockDebouncer{}, fakeClock, cache)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Use new UUID each time to force cache miss
		_, _ = engine.GetCurrentValue(ctx, uuid.New())
	}
}

// BenchmarkConfigCache_MemoryUsage benchmarks memory usage of the cache.
func BenchmarkConfigCache_MemoryUsage(b *testing.B) {
	benchmarks := []struct {
		name     string
		sessions int
	}{
		{"100_sessions", 100},
		{"1000_sessions", 1000},
		{"10000_sessions", 10000},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			clock := clockwork.NewFakeClock()
			testConfig := domain.ConfigSnapshot{
				ForTrigger:     "yes",
				AgainstTrigger: "no",
				LeftLabel:      "Against",
				RightLabel:     "For",
				DecaySpeed:     1.0,
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache := NewConfigCache(10*time.Second, clock)

				// Populate cache with N sessions
				for j := 0; j < bm.sessions; j++ {
					cache.Set(uuid.New(), testConfig)
				}

				// Force allocation tracking
				_ = cache.Size()
			}

			// Report memory usage
			b.ReportMetric(float64(bm.sessions), "sessions")
		})
	}
}

// BenchmarkConfigCache_Eviction benchmarks eviction performance.
func BenchmarkConfigCache_Eviction(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Populate cache with 1000 entries
	for i := 0; i < 1000; i++ {
		cache.Set(uuid.New(), testConfig)
	}

	// Advance time to expire all entries
	clock.Advance(11 * time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.EvictExpired()
	}
}

// BenchmarkConfigCache_ConcurrentReads benchmarks concurrent read performance.
func BenchmarkConfigCache_ConcurrentReads(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Pre-populate cache
	cache.Set(sessionUUID, testConfig)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.Get(sessionUUID)
		}
	})
}

// BenchmarkConfigCache_ConcurrentWrites benchmarks concurrent write performance.
func BenchmarkConfigCache_ConcurrentWrites(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		sessionUUID := uuid.New() // Each goroutine uses different UUID
		for pb.Next() {
			cache.Set(sessionUUID, testConfig)
		}
	})
}

// BenchmarkConfigCache_MixedReadWrite benchmarks mixed read/write workload.
func BenchmarkConfigCache_MixedReadWrite(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	sessionUUID := uuid.New()
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Pre-populate cache
	cache.Set(sessionUUID, testConfig)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				// 10% writes
				cache.Set(sessionUUID, testConfig)
			} else {
				// 90% reads
				_, _ = cache.Get(sessionUUID)
			}
			i++
		}
	})
}
