package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pscheid92/chatpulse/internal/domain"
)

const benchBroadcasterID = "broadcaster-bench"

func BenchmarkMemoryCache_Get(b *testing.B) {
	cache := newMemoryCache(10 * time.Second)
	broadcasterID := benchBroadcasterID
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	cache.set(broadcasterID, testConfig)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.get(broadcasterID)
	}
}

func BenchmarkMemoryCache_Set(b *testing.B) {
	cache := newMemoryCache(10 * time.Second)
	broadcasterID := benchBroadcasterID
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.set(broadcasterID, testConfig)
	}
}

func BenchmarkConfigCacheRepo_GetConfigByBroadcaster_MemoryHit(b *testing.B) {
	testCfg := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	// Create a ConfigCacheRepo with a mock that should never be called (L1 hit)
	configRepo := &mockConfigRepository{}
	repo := &ConfigCacheRepo{
		mem:     newMemoryCache(10 * time.Second),
		configs: configRepo,
	}

	// Pre-populate in-memory cache
	repo.mem.set(benchBroadcasterID, testCfg)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = repo.GetConfigByBroadcaster(ctx, benchBroadcasterID)
	}
}

func BenchmarkMemoryCache_MemoryUsage(b *testing.B) {
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
			testConfig := domain.OverlayConfig{
				MemorySeconds:  30,
				ForTrigger:     "yes",
				ForLabel:       "For",
				AgainstTrigger: "no",
				AgainstLabel:   "Against",
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache := newMemoryCache(10 * time.Second)

				for j := 0; j < bm.broadcasters; j++ {
					cache.set(fmt.Sprintf("broadcaster-%d", j), testConfig)
				}

				_ = cache.size()
			}

			b.ReportMetric(float64(bm.broadcasters), "broadcasters")
		})
	}
}

func BenchmarkMemoryCache_Eviction(b *testing.B) {
	cache := newMemoryCache(1 * time.Millisecond)
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	for i := range 1000 {
		cache.set(fmt.Sprintf("broadcaster-%d", i), testConfig)
	}

	time.Sleep(2 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.evictExpired()
	}
}

func BenchmarkMemoryCache_ConcurrentReads(b *testing.B) {
	cache := newMemoryCache(10 * time.Second)
	broadcasterID := benchBroadcasterID
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	cache.set(broadcasterID, testConfig)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.get(broadcasterID)
		}
	})
}

func BenchmarkMemoryCache_ConcurrentWrites(b *testing.B) {
	cache := newMemoryCache(10 * time.Second)
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		broadcasterID := fmt.Sprintf("broadcaster-%d", time.Now().UnixNano())
		for pb.Next() {
			cache.set(broadcasterID, testConfig)
		}
	})
}

func BenchmarkMemoryCache_MixedReadWrite(b *testing.B) {
	cache := newMemoryCache(10 * time.Second)
	broadcasterID := benchBroadcasterID
	testConfig := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
	}

	cache.set(broadcasterID, testConfig)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				cache.set(broadcasterID, testConfig)
			} else {
				_, _ = cache.get(broadcasterID)
			}
			i++
		}
	})
}
