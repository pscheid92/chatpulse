package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlobalConnectionLimiter_AcquireRelease(t *testing.T) {
	limiter := NewGlobalConnectionLimiter(3)

	// Acquire 3 slots (at limit)
	assert.True(t, limiter.Acquire())
	assert.True(t, limiter.Acquire())
	assert.True(t, limiter.Acquire())
	assert.Equal(t, int64(3), limiter.Current())

	// 4th acquire should fail
	assert.False(t, limiter.Acquire())
	assert.Equal(t, int64(3), limiter.Current())

	// Release one slot
	limiter.Release()
	assert.Equal(t, int64(2), limiter.Current())

	// Now acquire should succeed
	assert.True(t, limiter.Acquire())
	assert.Equal(t, int64(3), limiter.Current())
}

func TestGlobalConnectionLimiter_Concurrent(t *testing.T) {
	limiter := NewGlobalConnectionLimiter(100)
	var successCount, failCount int64

	// Barrier to ensure all goroutines try to acquire at roughly the same time
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // Wait for signal
			if limiter.Acquire() {
				atomic.AddInt64(&successCount, 1)
			} else {
				atomic.AddInt64(&failCount, 1)
			}
		}()
	}

	// Release all goroutines at once
	close(start)
	wg.Wait()

	// Should have exactly 100 successes and 100 failures
	assert.Equal(t, int64(100), atomic.LoadInt64(&successCount))
	assert.Equal(t, int64(100), atomic.LoadInt64(&failCount))
	assert.Equal(t, int64(100), limiter.Current())

	// Release all to verify counter works correctly
	for i := 0; i < 100; i++ {
		limiter.Release()
	}
	assert.Equal(t, int64(0), limiter.Current())
}

func TestGlobalConnectionLimiter_CapacityPct(t *testing.T) {
	limiter := NewGlobalConnectionLimiter(100)

	assert.Equal(t, 0.0, limiter.CapacityPct())

	for i := 0; i < 50; i++ {
		limiter.Acquire()
	}
	assert.Equal(t, 50.0, limiter.CapacityPct())

	for i := 0; i < 30; i++ {
		limiter.Acquire()
	}
	assert.Equal(t, 80.0, limiter.CapacityPct())
}

func TestGlobalConnectionLimiter_ZeroMax(t *testing.T) {
	limiter := NewGlobalConnectionLimiter(0)
	assert.False(t, limiter.Acquire())
	assert.Equal(t, 0.0, limiter.CapacityPct())
}

func TestIPConnectionLimiter_AcquireRelease(t *testing.T) {
	limiter := NewIPConnectionLimiter(2)

	// Acquire 2 slots for IP1
	assert.True(t, limiter.Acquire("192.168.1.1"))
	assert.True(t, limiter.Acquire("192.168.1.1"))
	assert.Equal(t, 2, limiter.Count("192.168.1.1"))

	// 3rd acquire for IP1 should fail
	assert.False(t, limiter.Acquire("192.168.1.1"))

	// Different IP should succeed
	assert.True(t, limiter.Acquire("192.168.1.2"))
	assert.Equal(t, 2, limiter.UniqueIPs())

	// Release from IP1
	limiter.Release("192.168.1.1")
	assert.Equal(t, 1, limiter.Count("192.168.1.1"))

	// Now IP1 can acquire again
	assert.True(t, limiter.Acquire("192.168.1.1"))
	assert.Equal(t, 2, limiter.Count("192.168.1.1"))
}

func TestIPConnectionLimiter_Cleanup(t *testing.T) {
	limiter := NewIPConnectionLimiter(5)

	// Acquire and release for IP1
	assert.True(t, limiter.Acquire("192.168.1.1"))
	assert.Equal(t, 1, limiter.UniqueIPs())

	limiter.Release("192.168.1.1")
	// After release to 0, IP should be removed
	assert.Equal(t, 0, limiter.UniqueIPs())
	assert.Equal(t, 0, limiter.Count("192.168.1.1"))
}

func TestIPConnectionLimiter_Concurrent(t *testing.T) {
	limiter := NewIPConnectionLimiter(10)
	var ip1Success, ip1Fail, ip2Success int64

	var wg sync.WaitGroup

	// 20 goroutines try to acquire for IP1 (limit 10)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Acquire("192.168.1.1") {
				atomic.AddInt64(&ip1Success, 1)
				time.Sleep(1 * time.Millisecond)
				limiter.Release("192.168.1.1")
			} else {
				atomic.AddInt64(&ip1Fail, 1)
			}
		}()
	}

	// 5 goroutines acquire for IP2 (should all succeed)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Acquire("192.168.1.2") {
				atomic.AddInt64(&ip2Success, 1)
				time.Sleep(1 * time.Millisecond)
				limiter.Release("192.168.1.2")
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(10), atomic.LoadInt64(&ip1Success))
	assert.Equal(t, int64(10), atomic.LoadInt64(&ip1Fail))
	assert.Equal(t, int64(5), atomic.LoadInt64(&ip2Success))
	assert.Equal(t, 0, limiter.UniqueIPs()) // All released
}

func TestConnectionRateLimiter_Allow(t *testing.T) {
	// Allow 2 per second, burst of 2
	limiter := NewConnectionRateLimiter(2.0, 2)

	// First 2 should succeed immediately (burst)
	assert.True(t, limiter.Allow("192.168.1.1"))
	assert.True(t, limiter.Allow("192.168.1.1"))

	// 3rd should fail (burst exhausted, no tokens yet)
	assert.False(t, limiter.Allow("192.168.1.1"))

	// Different IP should have its own limiter
	assert.True(t, limiter.Allow("192.168.1.2"))
	assert.Equal(t, 2, limiter.ActiveLimiters())
}

func TestConnectionRateLimiter_TokenRefill(t *testing.T) {
	// Allow 10 per second, burst of 5
	limiter := NewConnectionRateLimiter(10.0, 5)

	// Exhaust burst
	for i := 0; i < 5; i++ {
		assert.True(t, limiter.Allow("192.168.1.1"))
	}
	assert.False(t, limiter.Allow("192.168.1.1"))

	// Wait for token refill (100ms = 1 token at 10/sec)
	time.Sleep(100 * time.Millisecond)
	assert.True(t, limiter.Allow("192.168.1.1"))
}

func TestConnectionRateLimiter_PerIPIndependence(t *testing.T) {
	limiter := NewConnectionRateLimiter(2.0, 2)

	// IP1 exhausts burst
	assert.True(t, limiter.Allow("192.168.1.1"))
	assert.True(t, limiter.Allow("192.168.1.1"))
	assert.False(t, limiter.Allow("192.168.1.1"))

	// IP2 should still have full burst available
	assert.True(t, limiter.Allow("192.168.1.2"))
	assert.True(t, limiter.Allow("192.168.1.2"))
	assert.False(t, limiter.Allow("192.168.1.2"))
}

func TestConnectionRateLimiter_Cleanup(t *testing.T) {
	limiter := NewConnectionRateLimiter(10.0, 5)

	// Create limiters for multiple IPs
	limiter.Allow("192.168.1.1")
	limiter.Allow("192.168.1.2")
	limiter.Allow("192.168.1.3")
	assert.Equal(t, 3, limiter.ActiveLimiters())

	// Manually trigger cleanup (normally happens after 5 min)
	limiter.mu.Lock()
	limiter.cleanup()
	limiter.mu.Unlock()

	// Limiters created <10min ago should still exist
	assert.Equal(t, 3, limiter.ActiveLimiters())

	// Manually age one limiter
	limiter.mu.Lock()
	limiter.limiters["192.168.1.1"].lastSeen = time.Now().Add(-11 * time.Minute)
	limiter.cleanup()
	limiter.mu.Unlock()

	// One limiter should be removed
	assert.Equal(t, 2, limiter.ActiveLimiters())
}

func TestConnectionLimits_Acquire(t *testing.T) {
	limits := NewConnectionLimits(
		100, // global max
		10,  // per-IP max
		5.0, // 5 connections per second
		5,   // burst of 5
	)

	// Normal acquire should succeed
	ok, reason := limits.Acquire("192.168.1.1")
	assert.True(t, ok)
	assert.Equal(t, LimitReason(""), reason)

	// Release
	limits.Release("192.168.1.1")
}

func TestConnectionLimits_GlobalLimitExceeded(t *testing.T) {
	limits := NewConnectionLimits(
		2,     // global max: 2
		100,   // per-IP max
		100.0, // high rate limit
		100,   // high burst
	)

	// Acquire 2 slots
	ok1, _ := limits.Acquire("192.168.1.1")
	ok2, _ := limits.Acquire("192.168.1.2")
	assert.True(t, ok1)
	assert.True(t, ok2)

	// 3rd should fail with global limit
	ok3, reason := limits.Acquire("192.168.1.3")
	assert.False(t, ok3)
	assert.Equal(t, LimitReasonGlobal, reason)

	// Cleanup
	limits.Release("192.168.1.1")
	limits.Release("192.168.1.2")
}

func TestConnectionLimits_PerIPLimitExceeded(t *testing.T) {
	limits := NewConnectionLimits(
		100,   // global max
		2,     // per-IP max: 2
		100.0, // high rate limit
		100,   // high burst
	)

	// Acquire 2 slots for same IP
	ok1, _ := limits.Acquire("192.168.1.1")
	ok2, _ := limits.Acquire("192.168.1.1")
	assert.True(t, ok1)
	assert.True(t, ok2)

	// 3rd from same IP should fail
	ok3, reason := limits.Acquire("192.168.1.1")
	assert.False(t, ok3)
	assert.Equal(t, LimitReasonPerIP, reason)

	// Different IP should succeed
	ok4, _ := limits.Acquire("192.168.1.2")
	assert.True(t, ok4)

	// Cleanup
	limits.Release("192.168.1.1")
	limits.Release("192.168.1.1")
	limits.Release("192.168.1.2")
}

func TestConnectionLimits_RateLimitExceeded(t *testing.T) {
	limits := NewConnectionLimits(
		100, // global max
		100, // per-IP max
		2.0, // 2 per second
		2,   // burst of 2
	)

	// Exhaust burst (2 immediate connections)
	ok1, _ := limits.Acquire("192.168.1.1")
	ok2, _ := limits.Acquire("192.168.1.1")
	assert.True(t, ok1)
	assert.True(t, ok2)

	// 3rd should fail with rate limit
	ok3, reason := limits.Acquire("192.168.1.1")
	assert.False(t, ok3)
	assert.Equal(t, LimitReasonRate, reason)

	// Cleanup
	limits.Release("192.168.1.1")
	limits.Release("192.168.1.1")
}

func TestConnectionLimits_RollbackOnFailure(t *testing.T) {
	limits := NewConnectionLimits(
		100,   // global max
		1,     // per-IP max: 1 (will cause failure)
		100.0, // high rate
		100,   // high burst
	)

	// First acquire succeeds
	ok1, _ := limits.Acquire("192.168.1.1")
	assert.True(t, ok1)
	assert.Equal(t, int64(1), limits.Global().Current())

	// Second acquire for same IP fails at per-IP check
	ok2, reason := limits.Acquire("192.168.1.1")
	assert.False(t, ok2)
	assert.Equal(t, LimitReasonPerIP, reason)

	// Global counter should be rolled back (still 1, not 2)
	assert.Equal(t, int64(1), limits.Global().Current())

	// Cleanup
	limits.Release("192.168.1.1")
	assert.Equal(t, int64(0), limits.Global().Current())
}

func TestConnectionLimits_Concurrent(t *testing.T) {
	limits := NewConnectionLimits(
		50,    // global max: 50
		5,     // per-IP max: 5
		100.0, // high rate (won't be hit in this test)
		100,   // high burst
	)

	var wg sync.WaitGroup
	successCount := int64(0)
	var mu sync.Mutex

	// 10 IPs, each trying 10 connections = 100 attempts
	// Should get: 10 IPs * min(5 per-IP, 50 global / 10 IPs) = 10 * 5 = 50 successes
	for ip := 1; ip <= 10; ip++ {
		for conn := 0; conn < 10; conn++ {
			wg.Add(1)
			ipAddr := "192.168.1." + string(rune('0'+ip))
			go func(ip string) {
				defer wg.Done()
				if ok, _ := limits.Acquire(ip); ok {
					mu.Lock()
					successCount++
					mu.Unlock()
					time.Sleep(1 * time.Millisecond)
					limits.Release(ip)
				}
			}(ipAddr)
		}
	}

	wg.Wait()

	// Should have exactly 50 successes (global limit)
	assert.Equal(t, int64(50), successCount)
	assert.Equal(t, int64(0), limits.Global().Current())
}

func TestConnectionLimits_Accessors(t *testing.T) {
	limits := NewConnectionLimits(100, 10, 5.0, 5)

	require.NotNil(t, limits.Global())
	require.NotNil(t, limits.PerIP())
	require.NotNil(t, limits.Rate())

	assert.Equal(t, int64(100), limits.Global().Max())
	assert.Equal(t, 10, limits.PerIP().MaxPer())
	assert.Equal(t, 5.0, limits.Rate().Rate())
	assert.Equal(t, 5, limits.Rate().Burst())
}
