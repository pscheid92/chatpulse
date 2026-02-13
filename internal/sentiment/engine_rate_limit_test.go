package sentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessVote_RateLimited verifies votes are rejected when rate limited.
func TestProcessVote_RateLimited(t *testing.T) {
	clock := clockwork.NewFakeClock()
	configSource, sentiment, debounce := newHappyPathMocks()

	// Rate limiter rejects the vote
	rateLimiter := &mockVoteRateLimiter{
		checkVoteRateLimitFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil // Reject
		},
	}

	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, rateLimiter, clock, testCache)

	initialRateLimited := testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("rate_limited"))

	value, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")

	require.NoError(t, err)
	assert.Equal(t, domain.VoteRateLimited, result)
	assert.Equal(t, 0.0, value)

	// Verify metric incremented
	assert.Equal(t, initialRateLimited+1, testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("rate_limited")))
}

// TestProcessVote_RateLimitAllowed verifies votes proceed when rate limit allows.
func TestProcessVote_RateLimitAllowed(t *testing.T) {
	clock := clockwork.NewFakeClock()
	configSource, sentiment, debounce := newHappyPathMocks()

	// Rate limiter allows the vote
	rateLimiter := &mockVoteRateLimiter{
		checkVoteRateLimitFn: func(_ context.Context, _ string) (bool, error) {
			return true, nil // Allow
		},
	}

	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, rateLimiter, clock, testCache)

	initialApplied := testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("applied"))

	value, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")

	require.NoError(t, err)
	assert.Equal(t, domain.VoteApplied, result)
	assert.Equal(t, 10.0, value)

	// Verify metric incremented
	assert.Equal(t, initialApplied+1, testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("applied")))
}

// TestProcessVote_RateLimitError_FailOpen verifies fail-open behavior on Redis error.
func TestProcessVote_RateLimitError_FailOpen(t *testing.T) {
	clock := clockwork.NewFakeClock()
	configSource, sentiment, debounce := newHappyPathMocks()

	// Rate limiter returns error (simulates Redis failure)
	rateLimiter := &mockVoteRateLimiter{
		checkVoteRateLimitFn: func(_ context.Context, _ string) (bool, error) {
			return false, errors.New("redis connection error")
		},
	}

	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, rateLimiter, clock, testCache)

	initialApplied := testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("applied"))

	value, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")

	// Vote should be ALLOWED (fail-open for availability)
	require.NoError(t, err)
	assert.Equal(t, domain.VoteApplied, result)
	assert.Equal(t, 10.0, value)

	// Verify metric incremented (vote was applied despite error)
	assert.Equal(t, initialApplied+1, testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("applied")))
}

// TestProcessVote_RateLimitAfterDebounce verifies rate limit runs after debounce check.
func TestProcessVote_RateLimitAfterDebounce(t *testing.T) {
	clock := clockwork.NewFakeClock()
	configSource, sentiment, _ := newHappyPathMocks()

	// Debouncer rejects the vote
	debounce := &mockDebouncer{
		checkDebounceFn: func(_ context.Context, _ string, _ string) (bool, error) {
			return false, nil // Debounced
		},
	}

	var rateLimitCalled bool
	rateLimiter := &mockVoteRateLimiter{
		checkVoteRateLimitFn: func(_ context.Context, _ string) (bool, error) {
			rateLimitCalled = true
			return true, nil
		},
	}

	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, rateLimiter, clock, testCache)

	initialDebounced := testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("debounced"))

	value, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")

	require.NoError(t, err)
	assert.Equal(t, domain.VoteDebounced, result)
	assert.Equal(t, 0.0, value)

	// Rate limiter should NOT be called (debounce failed first)
	assert.False(t, rateLimitCalled, "Rate limiter should not be called when debounce rejects")

	// Verify debounced metric incremented
	assert.Equal(t, initialDebounced+1, testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("debounced")))
}

// TestProcessVote_RateLimitWithNoTriggerMatch verifies rate limit not checked for non-matching messages.
func TestProcessVote_RateLimitWithNoTriggerMatch(t *testing.T) {
	clock := clockwork.NewFakeClock()
	configSource, sentiment, debounce := newHappyPathMocks()

	var rateLimitCalled bool
	rateLimiter := &mockVoteRateLimiter{
		checkVoteRateLimitFn: func(_ context.Context, _ string) (bool, error) {
			rateLimitCalled = true
			return true, nil
		},
	}

	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, rateLimiter, clock, testCache)

	initialNoMatch := testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("no_match"))

	// Message doesn't match trigger words
	value, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "hello world")

	require.NoError(t, err)
	assert.Equal(t, domain.VoteNoMatch, result)
	assert.Equal(t, 0.0, value)

	// Rate limiter should NOT be called (no trigger match)
	assert.False(t, rateLimitCalled, "Rate limiter should not be called when trigger doesn't match")

	// Verify no_match metric incremented
	assert.Equal(t, initialNoMatch+1, testutil.ToFloat64(metrics.VoteProcessingTotal.WithLabelValues("no_match")))
}
