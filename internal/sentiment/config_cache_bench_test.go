package sentiment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const benchBroadcasterID = "broadcaster-bench"

// BenchmarkConfigCache_Get benchmarks cache read performance.
func BenchmarkConfigCache_Get(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	broadcasterID := benchBroadcasterID
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Pre-populate cache
	cache.Set(broadcasterID, testConfig)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(broadcasterID)
	}
}

// BenchmarkConfigCache_Set benchmarks cache write performance.
func BenchmarkConfigCache_Set(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	broadcasterID := benchBroadcasterID
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Set(broadcasterID, testConfig)
	}
}

// BenchmarkEngine_GetBroadcastData_CacheHit benchmarks GetBroadcastData with cache hit.
func BenchmarkEngine_GetBroadcastData_CacheHit(b *testing.B) {
	fakeClock := clockwork.NewFakeClock()
	testCfg := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	configSource := &mockConfigSource{
		getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
			return testCfg, nil
		},
	}

	sentimentStore := &mockSentimentStore{}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(configSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, cache)

	ctx := context.Background()

	// Warm up cache with one call
	_, _ = engine.GetBroadcastData(ctx, benchBroadcasterID)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.GetBroadcastData(ctx, benchBroadcasterID)
	}
}

// BenchmarkEngine_GetBroadcastData_CacheMiss benchmarks GetBroadcastData with cache miss.
func BenchmarkEngine_GetBroadcastData_CacheMiss(b *testing.B) {
	fakeClock := clockwork.NewFakeClock()
	testCfg := &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	configSource := &mockConfigSource{
		getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
			return testCfg, nil
		},
	}

	sentimentStore := &mockSentimentStore{}

	cache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(configSource, sentimentStore, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, cache)

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Use new broadcaster ID each time to force cache miss
		_, _ = engine.GetBroadcastData(ctx, fmt.Sprintf("broadcaster-miss-%d", i))
	}
}

// BenchmarkConfigCache_MemoryUsage benchmarks memory usage of the cache.
func BenchmarkConfigCache_MemoryUsage(b *testing.B) {
	benchmarks := []struct {
		name         string
		broadcasters int
	}{
		{"100_broadcasters", 100},
		{"1000_broadcasters", 1000},
		{"10000_broadcasters", 10000},
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

				// Populate cache with N broadcasters
				for j := 0; j < bm.broadcasters; j++ {
					cache.Set(fmt.Sprintf("broadcaster-%d", j), testConfig)
				}

				// Force allocation tracking
				_ = cache.Size()
			}

			// Report memory usage
			b.ReportMetric(float64(bm.broadcasters), "broadcasters")
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
		cache.Set(fmt.Sprintf("broadcaster-%d", i), testConfig)
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
	broadcasterID := benchBroadcasterID
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Pre-populate cache
	cache.Set(broadcasterID, testConfig)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.Get(broadcasterID)
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
		broadcasterID := fmt.Sprintf("broadcaster-%d", time.Now().UnixNano())
		for pb.Next() {
			cache.Set(broadcasterID, testConfig)
		}
	})
}

// BenchmarkConfigCache_MixedReadWrite benchmarks mixed read/write workload.
func BenchmarkConfigCache_MixedReadWrite(b *testing.B) {
	clock := clockwork.NewFakeClock()
	cache := NewConfigCache(10*time.Second, clock)
	broadcasterID := benchBroadcasterID
	testConfig := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Pre-populate cache
	cache.Set(broadcasterID, testConfig)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				// 10% writes
				cache.Set(broadcasterID, testConfig)
			} else {
				// 90% reads
				_, _ = cache.Get(broadcasterID)
			}
			i++
		}
	})
}
