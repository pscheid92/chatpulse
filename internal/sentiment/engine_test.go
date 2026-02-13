package sentiment

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock SessionRepo (only session query methods used by Engine) ---

type mockSessionRepo struct {
	getSessionConfigFn        func(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error)
	getSessionByBroadcasterFn func(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
}

func (m *mockSessionRepo) GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
	if m.getSessionConfigFn != nil {
		return m.getSessionConfigFn(ctx, sessionUUID)
	}
	return nil, nil
}

func (m *mockSessionRepo) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	if m.getSessionByBroadcasterFn != nil {
		return m.getSessionByBroadcasterFn(ctx, broadcasterUserID)
	}
	return uuid.Nil, false, nil
}

// Stub methods to satisfy domain.SessionRepository.
func (m *mockSessionRepo) ActivateSession(context.Context, uuid.UUID, string, domain.ConfigSnapshot) error {
	return nil
}
func (m *mockSessionRepo) ResumeSession(context.Context, uuid.UUID) error         { return nil }
func (m *mockSessionRepo) SessionExists(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (m *mockSessionRepo) DeleteSession(context.Context, uuid.UUID) error         { return nil }
func (m *mockSessionRepo) MarkDisconnected(context.Context, uuid.UUID) error      { return nil }
func (m *mockSessionRepo) UpdateConfig(context.Context, uuid.UUID, domain.ConfigSnapshot) error {
	return nil
}
func (m *mockSessionRepo) IncrRefCount(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (m *mockSessionRepo) DecrRefCount(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (m *mockSessionRepo) DisconnectedCount(context.Context) (int64, error)       { return 0, nil }
func (m *mockSessionRepo) ListOrphans(context.Context, time.Duration) ([]uuid.UUID, error) {
	return nil, nil
}

func (m *mockSessionRepo) ListActiveSessions(context.Context) ([]domain.ActiveSession, error) {
	return nil, nil
}

// --- Mock SentimentStore ---

type mockSentimentStore struct {
	getSentimentFn   func(ctx context.Context, sessionUUID uuid.UUID, decayRate float64, nowMs int64) (float64, error)
	applyVoteFn      func(ctx context.Context, sessionUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error)
	resetSentimentFn func(ctx context.Context, sessionUUID uuid.UUID) error
}

func (m *mockSentimentStore) GetSentiment(ctx context.Context, sessionUUID uuid.UUID, decayRate float64, nowMs int64) (float64, error) {
	if m.getSentimentFn != nil {
		return m.getSentimentFn(ctx, sessionUUID, decayRate, nowMs)
	}
	return 0, nil
}

func (m *mockSentimentStore) ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error) {
	if m.applyVoteFn != nil {
		return m.applyVoteFn(ctx, sessionUUID, delta, decayRate, nowMs)
	}
	return 0, nil
}

func (m *mockSentimentStore) ResetSentiment(ctx context.Context, sessionUUID uuid.UUID) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, sessionUUID)
	}
	return nil
}

// --- Mock Debouncer ---

type mockDebouncer struct {
	checkDebounceFn func(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error)
}

func (m *mockDebouncer) CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error) {
	if m.checkDebounceFn != nil {
		return m.checkDebounceFn(ctx, sessionUUID, twitchUserID)
	}
	return false, nil
}

// --- Mock Vote Rate Limiter ---

type mockVoteRateLimiter struct {
	checkVoteRateLimitFn func(ctx context.Context, sessionUUID uuid.UUID) (bool, error)
}

func (m *mockVoteRateLimiter) CheckVoteRateLimit(ctx context.Context, sessionUUID uuid.UUID) (bool, error) {
	if m.checkVoteRateLimitFn != nil {
		return m.checkVoteRateLimitFn(ctx, sessionUUID)
	}
	return true, nil // Default: allow votes
}

// --- GetCurrentValue Tests ---

func TestGetCurrentValue_ReturnsDecayedValue(t *testing.T) {
	var lastDecayRate float64
	var lastNowMs int64

	fakeClock := clockwork.NewFakeClock()
	sessions := &mockSessionRepo{
		getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
			return &domain.ConfigSnapshot{DecaySpeed: 0.5}, nil
		},
	}
	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, decayRate float64, nowMs int64) (float64, error) {
			lastDecayRate = decayRate
			lastNowMs = nowMs
			return 42.5, nil
		},
	}
	testCache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(sessions, sentiment, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, testCache)

	val, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 42.5, val)
	assert.Equal(t, 0.5, lastDecayRate)
	assert.Equal(t, fakeClock.Now().UnixMilli(), lastNowMs)
}

func TestGetCurrentValue_NoSession(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(&mockSessionRepo{}, &mockSentimentStore{}, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, testCache)

	val, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 0.0, val)
}

func TestGetCurrentValue_UsesClockTime(t *testing.T) {
	var lastNowMs int64

	fakeClock := clockwork.NewFakeClock()
	sessions := &mockSessionRepo{
		getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
			return &domain.ConfigSnapshot{DecaySpeed: 1.0}, nil
		},
	}
	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, nowMs int64) (float64, error) {
			lastNowMs = nowMs
			return 10.0, nil
		},
	}
	testCache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(sessions, sentiment, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, testCache)

	expectedMs := fakeClock.Now().UnixMilli()
	_, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, expectedMs, lastNowMs)
}

func TestGetCurrentValue_DecayMath(t *testing.T) {
	decayRate := 0.5
	initialValue := 80.0
	dtSeconds := 2.0
	expected := initialValue * math.Exp(-decayRate*dtSeconds)

	fakeClock := clockwork.NewFakeClock()
	sessions := &mockSessionRepo{
		getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
			return &domain.ConfigSnapshot{DecaySpeed: decayRate}, nil
		},
	}
	sentiment := &mockSentimentStore{
		getSentimentFn: func(_ context.Context, _ uuid.UUID, _ float64, _ int64) (float64, error) {
			return expected, nil
		},
	}
	testCache := NewConfigCache(10*time.Second, fakeClock)
	engine := NewEngine(sessions, sentiment, &mockDebouncer{}, &mockVoteRateLimiter{}, fakeClock, testCache)

	val, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.InDelta(t, expected, val, 0.001)
}

// --- ProcessVote Tests ---

var (
	testUUID   = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	testConfig = &domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}
)

func newHappyPathMocks() (*mockSessionRepo, *mockSentimentStore, *mockDebouncer) {
	sessions := &mockSessionRepo{
		getSessionByBroadcasterFn: func(_ context.Context, _ string) (uuid.UUID, bool, error) {
			return testUUID, true, nil
		},
		getSessionConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
			return testConfig, nil
		},
	}
	sentiment := &mockSentimentStore{
		applyVoteFn: func(_ context.Context, _ uuid.UUID, delta, _ float64, _ int64) (float64, error) {
			return delta, nil
		},
	}
	debounce := &mockDebouncer{
		checkDebounceFn: func(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
			return true, nil
		},
	}
	return sessions, sentiment, debounce
}

func TestProcessVote_Success(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.True(t, applied)
	assert.Equal(t, 10.0, val)
}

func TestProcessVote_SessionNotFound(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	sessions.getSessionByBroadcasterFn = func(_ context.Context, _ string) (uuid.UUID, bool, error) {
		return uuid.Nil, false, nil
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.False(t, applied)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_SessionLookupError(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	sessions.getSessionByBroadcasterFn = func(_ context.Context, _ string) (uuid.UUID, bool, error) {
		return uuid.Nil, false, errors.New("redis error")
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.False(t, applied)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_ConfigNotFound(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	sessions.getSessionConfigFn = func(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
		return nil, nil
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.False(t, applied)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_NoTriggerMatch(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "hello world")
	assert.False(t, applied)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_Debounced(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	debounce.checkDebounceFn = func(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
		return false, nil
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.False(t, applied)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_ApplyVoteError(t *testing.T) {
	sessions, sentiment, debounce := newHappyPathMocks()
	sentiment.applyVoteFn = func(_ context.Context, _ uuid.UUID, _, _ float64, _ int64) (float64, error) {
		return 0, errors.New("lua script error")
	}
	clock := clockwork.NewFakeClock()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	val, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.False(t, applied)
	assert.Equal(t, 0.0, val)
}

func TestProcessVote_UsesClockTime(t *testing.T) {
	var capturedNowMs int64
	sessions, _, debounce := newHappyPathMocks()
	sentiment := &mockSentimentStore{
		applyVoteFn: func(_ context.Context, _ uuid.UUID, delta, _ float64, nowMs int64) (float64, error) {
			capturedNowMs = nowMs
			return delta, nil
		},
	}

	clock := clockwork.NewFakeClock()
	expectedMs := clock.Now().UnixMilli()
	testCache := NewConfigCache(10*time.Second, clock)
	engine := NewEngine(sessions, sentiment, debounce, &mockVoteRateLimiter{}, clock, testCache)

	_, applied := engine.ProcessVote(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.True(t, applied)
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
