package sentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock ConfigSource ---

type mockConfigSource struct {
	getConfigByBroadcasterFn func(ctx context.Context, broadcasterID string) (*domain.ConfigSnapshot, error)
}

func (m *mockConfigSource) GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (*domain.ConfigSnapshot, error) {
	if m.getConfigByBroadcasterFn != nil {
		return m.getConfigByBroadcasterFn(ctx, broadcasterID)
	}
	return nil, nil
}

// --- Mock SentimentStore ---

type mockSentimentStore struct {
	getSentimentFn   func(ctx context.Context, broadcasterID string, decayRate float64, nowMs int64) (float64, error)
	applyVoteFn      func(ctx context.Context, broadcasterID string, delta, decayRate float64, nowMs int64) (float64, error)
	resetSentimentFn func(ctx context.Context, broadcasterID string) error
}

func (m *mockSentimentStore) GetSentiment(ctx context.Context, broadcasterID string, decayRate float64, nowMs int64) (float64, error) {
	if m.getSentimentFn != nil {
		return m.getSentimentFn(ctx, broadcasterID, decayRate, nowMs)
	}
	return 0, nil
}

func (m *mockSentimentStore) ApplyVote(ctx context.Context, broadcasterID string, delta, decayRate float64, nowMs int64) (float64, error) {
	if m.applyVoteFn != nil {
		return m.applyVoteFn(ctx, broadcasterID, delta, decayRate, nowMs)
	}
	return 0, nil
}

func (m *mockSentimentStore) GetRawSentiment(_ context.Context, _ string) (float64, int64, error) {
	return 0, 0, nil
}

func (m *mockSentimentStore) ResetSentiment(ctx context.Context, broadcasterID string) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, broadcasterID)
	}
	return nil
}

// --- Mock Debouncer ---

type mockDebouncer struct {
	checkDebounceFn func(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error)
}

func (m *mockDebouncer) CheckDebounce(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error) {
	if m.checkDebounceFn != nil {
		return m.checkDebounceFn(ctx, broadcasterID, twitchUserID)
	}
	return false, nil
}

// --- Mock Vote Rate Limiter ---

type mockVoteRateLimiter struct {
	checkVoteRateLimitFn func(ctx context.Context, broadcasterID string) (bool, error)
}

func (m *mockVoteRateLimiter) CheckVoteRateLimit(ctx context.Context, broadcasterID string) (bool, error) {
	if m.checkVoteRateLimitFn != nil {
		return m.checkVoteRateLimitFn(ctx, broadcasterID)
	}
	return true, nil // Default: allow votes
}

// --- ProcessVote Tests ---

var testConfig = &domain.ConfigSnapshot{
	ForTrigger:     "yes",
	AgainstTrigger: "no",
	DecaySpeed:     1.0,
}

func newHappyPathMocks() (*mockConfigSource, *mockSentimentStore, *mockDebouncer) {
	configSource := &mockConfigSource{
		getConfigByBroadcasterFn: func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
			return testConfig, nil
		},
	}
	sentiment := &mockSentimentStore{
		applyVoteFn: func(_ context.Context, _ string, delta, _ float64, _ int64) (float64, error) {
			return delta, nil
		},
	}
	debounce := &mockDebouncer{
		checkDebounceFn: func(_ context.Context, _ string, _ string) (bool, error) {
			return true, nil
		},
	}
	return configSource, sentiment, debounce
}

func TestProcessVote_Success(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteApplied, result)
	assert.Equal(t, 10.0, val)
}

func TestProcessVote_NoConfig(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	configSource.getConfigByBroadcasterFn = func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
		return nil, nil
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteNoSession, result)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_ConfigLookupError(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	configSource.getConfigByBroadcasterFn = func(_ context.Context, _ string) (*domain.ConfigSnapshot, error) {
		return nil, errors.New("redis error")
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.Error(t, err)
	assert.Equal(t, domain.VoteNoSession, result)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_NoTriggerMatch(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "hello world")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteNoMatch, result)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_Debounced(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	debounce.checkDebounceFn = func(_ context.Context, _ string, _ string) (bool, error) {
		return false, nil
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteDebounced, result)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_ApplyVoteError(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	sentiment.applyVoteFn = func(_ context.Context, _ string, _, _ float64, _ int64) (float64, error) {
		return 0, errors.New("lua script error")
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.Error(t, err)
	assert.Equal(t, domain.VoteError, result)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_UsesClockTime(t *testing.T) {
	var capturedNowMs int64
	configSource, _, debounce := newHappyPathMocks()
	sentiment := &mockSentimentStore{
		applyVoteFn: func(_ context.Context, _ string, delta, _ float64, nowMs int64) (float64, error) {
			capturedNowMs = nowMs
			return delta, nil
		},
	}

	clock := clockwork.NewFakeClock()
	expectedMs := clock.Now().UnixMilli()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(configSource, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	_, result, err := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteApplied, result)
	assert.Equal(t, expectedMs, capturedNowMs)
}

// --- matchTrigger Tests ---

func TestMatchTrigger_ForTrigger(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, matchTrigger("yes", config))
}

func TestMatchTrigger_AgainstTrigger(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, -10.0, matchTrigger("no", config))
}

func TestMatchTrigger_NoMatch(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, matchTrigger("hello world", config))
}

func TestMatchTrigger_CaseInsensitive(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, matchTrigger("YES", config))
	assert.Equal(t, -10.0, matchTrigger("NO", config))
}

func TestMatchTrigger_ForTriggerPriority(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "vote", AgainstTrigger: "vote"}
	// When both triggers are the same, ForTrigger takes priority
	assert.Equal(t, 10.0, matchTrigger("vote", config))
}

func TestMatchTrigger_NilConfig(t *testing.T) {
	assert.Equal(t, 0.0, matchTrigger("yes", nil))
}

func TestMatchTrigger_EmptyMessage(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, matchTrigger("", config))
}

func TestMatchTrigger_EmptyTriggers(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "", AgainstTrigger: ""}
	assert.Equal(t, 0.0, matchTrigger("hello world", config))
	assert.Equal(t, 0.0, matchTrigger("yes", config))
	assert.Equal(t, 0.0, matchTrigger("no", config))
}

func TestMatchTrigger_OneEmptyTrigger(t *testing.T) {
	// Empty for-trigger should not match, but against-trigger should
	config := &domain.ConfigSnapshot{ForTrigger: "", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, matchTrigger("hello", config))
	assert.Equal(t, -10.0, matchTrigger("no", config))

	// Empty against-trigger should not match, but for-trigger should
	config2 := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: ""}
	assert.Equal(t, 10.0, matchTrigger("yes", config2))
	assert.Equal(t, 0.0, matchTrigger("hello", config2))
}

func TestMatchTrigger_SubstringDoesNotMatch(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 0.0, matchTrigger("I vote yes!", config))
	assert.Equal(t, 0.0, matchTrigger("vote no", config))
	assert.Equal(t, 0.0, matchTrigger("yes please", config))
	assert.Equal(t, 0.0, matchTrigger("nope", config))
}

func TestMatchTrigger_WhitespaceHandling(t *testing.T) {
	config := &domain.ConfigSnapshot{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, 10.0, matchTrigger("  yes  ", config))
	assert.Equal(t, -10.0, matchTrigger("\tno\t", config))
	assert.Equal(t, 10.0, matchTrigger("  YES  ", config))
}
