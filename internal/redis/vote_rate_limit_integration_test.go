package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVoteRateLimit_Integration_InitialBurst verifies 100-vote burst allowance.
func TestVoteRateLimit_Integration_InitialBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcasterID := "broadcaster-burst-test"

	// Should allow 100 votes immediately (burst capacity)
	for i := 0; i < 100; i++ {
		allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
		assert.True(t, allowed, "Vote %d should be allowed (burst)", i+1)
	}

	// 101st vote should be rejected (bucket empty)
	allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
	require.NoError(t, err)
	assert.False(t, allowed, "Vote 101 should be rejected (bucket exhausted)")
}

// TestVoteRateLimit_Integration_Refill verifies token refill over time.
func TestVoteRateLimit_Integration_Refill(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcasterID := "broadcaster-refill-test"

	// Exhaust bucket (100 votes)
	for i := 0; i < 100; i++ {
		_, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
	}

	// Verify bucket is empty
	allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
	require.NoError(t, err)
	assert.False(t, allowed, "Bucket should be exhausted")

	// Advance time by 60 seconds (should refill 100 tokens)
	clock.Advance(60 * time.Second)

	// Should allow 100 votes again (bucket refilled)
	for i := 0; i < 100; i++ {
		allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
		assert.True(t, allowed, "Vote %d should be allowed after refill", i+1)
	}

	// 101st vote rejected again
	allowed, err = rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
	require.NoError(t, err)
	assert.False(t, allowed, "Vote 101 should be rejected after refill")
}

// TestVoteRateLimit_Integration_PartialRefill verifies partial token refill.
func TestVoteRateLimit_Integration_PartialRefill(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcasterID := "broadcaster-partial-test"

	// Use 50 tokens
	for i := 0; i < 50; i++ {
		_, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
	}

	// Advance time by 30 seconds (should refill 50 tokens: 30s * 100/60 = 50)
	clock.Advance(30 * time.Second)

	// Should have 100 tokens again (50 remaining + 50 refilled, capped at 100)
	for i := 0; i < 100; i++ {
		allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
		assert.True(t, allowed, "Vote %d should be allowed", i+1)
	}

	// 101st vote rejected
	allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
	require.NoError(t, err)
	assert.False(t, allowed, "Vote 101 should be rejected")
}

// TestVoteRateLimit_Integration_SustainedRate verifies sustained 100/min rate.
func TestVoteRateLimit_Integration_SustainedRate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcasterID := "broadcaster-sustained-test"

	// Exhaust initial burst
	for i := 0; i < 100; i++ {
		_, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
	}

	// Over 3 minutes, should allow exactly 300 votes (100 per minute)
	totalAllowed := 0
	for minute := 0; minute < 3; minute++ {
		// Advance 1 minute
		clock.Advance(60 * time.Second)

		// Try 150 votes (only 100 should succeed per minute)
		for i := 0; i < 150; i++ {
			allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
			require.NoError(t, err)
			if allowed {
				totalAllowed++
			}
		}
	}

	// Should have allowed exactly 300 votes (100 per minute × 3 minutes)
	assert.Equal(t, 300, totalAllowed, "Sustained rate should be 100 votes/minute")
}

// TestVoteRateLimit_Integration_TTL verifies 5-minute TTL on rate limit keys.
func TestVoteRateLimit_Integration_TTL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcasterID := "broadcaster-ttl-test"
	key := fmt.Sprintf("rate_limit:votes:%s", broadcasterID)

	// Make one vote to create the key
	_, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
	require.NoError(t, err)

	// Check TTL is set (should be 300 seconds = 5 minutes)
	ttl := client.TTL(ctx, key).Val()
	assert.Greater(t, ttl.Seconds(), float64(290), "TTL should be ~300 seconds")
	assert.LessOrEqual(t, ttl.Seconds(), float64(300), "TTL should not exceed 300 seconds")
}

// TestVoteRateLimit_Integration_PerBroadcaster verifies rate limits are per-broadcaster.
func TestVoteRateLimit_Integration_PerBroadcaster(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcaster1 := "broadcaster-1"
	broadcaster2 := "broadcaster-2"

	// Exhaust bucket for broadcaster1
	for i := 0; i < 100; i++ {
		_, err := rateLimiter.CheckVoteRateLimit(ctx, broadcaster1)
		require.NoError(t, err)
	}

	// broadcaster1 should be rate limited
	allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcaster1)
	require.NoError(t, err)
	assert.False(t, allowed, "Broadcaster1 should be rate limited")

	// broadcaster2 should still have full capacity (separate bucket)
	allowed, err = rateLimiter.CheckVoteRateLimit(ctx, broadcaster2)
	require.NoError(t, err)
	assert.True(t, allowed, "Broadcaster2 should have independent rate limit")
}

// TestVoteRateLimit_Integration_ZeroTimeDelta verifies no tokens added when no time passes.
func TestVoteRateLimit_Integration_ZeroTimeDelta(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	rateLimiter := NewVoteRateLimiter(client, clock, 100, 100)

	ctx := context.Background()
	broadcasterID := "broadcaster-zero-delta-test"

	// Exhaust bucket
	for i := 0; i < 100; i++ {
		_, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
		require.NoError(t, err)
	}

	// No time passes - still rate limited
	allowed, err := rateLimiter.CheckVoteRateLimit(ctx, broadcasterID)
	require.NoError(t, err)
	assert.False(t, allowed, "Should remain rate limited when no time passes")
}
