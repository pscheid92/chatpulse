package app

import (
	"context"
	"errors"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock ConfigSource ---

type mockConfigSource struct {
	getConfigByBroadcasterFn func(ctx context.Context, broadcasterID string) (domain.OverlayConfig, error)
}

func (m *mockConfigSource) GetConfigByBroadcaster(ctx context.Context, broadcasterID string) (domain.OverlayConfig, error) {
	if m.getConfigByBroadcasterFn != nil {
		return m.getConfigByBroadcasterFn(ctx, broadcasterID)
	}
	return domain.OverlayConfig{}, domain.ErrConfigNotFound
}

// --- Mock SentimentStore ---

type mockSentimentStore struct {
	recordVoteFn     func(ctx context.Context, broadcasterID string, target domain.VoteTarget, windowSeconds int) (*domain.WindowSnapshot, error)
	getSnapshotFn    func(ctx context.Context, broadcasterID string, windowSeconds int) (*domain.WindowSnapshot, error)
	resetSentimentFn func(ctx context.Context, broadcasterID string) error
}

func (m *mockSentimentStore) RecordVote(ctx context.Context, broadcasterID string, target domain.VoteTarget, windowSeconds int) (*domain.WindowSnapshot, error) {
	if m.recordVoteFn != nil {
		return m.recordVoteFn(ctx, broadcasterID, target, windowSeconds)
	}
	return &domain.WindowSnapshot{}, nil
}

func (m *mockSentimentStore) GetSnapshot(ctx context.Context, broadcasterID string, windowSeconds int) (*domain.WindowSnapshot, error) {
	if m.getSnapshotFn != nil {
		return m.getSnapshotFn(ctx, broadcasterID, windowSeconds)
	}
	return &domain.WindowSnapshot{}, nil
}

func (m *mockSentimentStore) ResetSentiment(ctx context.Context, broadcasterID string) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, broadcasterID)
	}
	return nil
}

// --- Mock ParticipantDebouncer ---

type mockDebouncer struct {
	isDebouncedFn func(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error)
}

func (m *mockDebouncer) IsDebounced(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error) {
	if m.isDebouncedFn != nil {
		return m.isDebouncedFn(ctx, broadcasterID, twitchUserID)
	}
	return true, nil
}

// --- ProcessMessage Tests ---

var testConfig = domain.OverlayConfig{
	MemorySeconds:  30,
	ForTrigger:     "yes",
	AgainstTrigger: "no",
	DisplayMode:    domain.DisplayModeCombined,
}

func newHappyPathMocks() (*mockConfigSource, *mockSentimentStore, *mockDebouncer) {
	configSource := &mockConfigSource{
		getConfigByBroadcasterFn: func(_ context.Context, _ string) (domain.OverlayConfig, error) {
			return testConfig, nil
		},
	}
	sentiment := &mockSentimentStore{
		recordVoteFn: func(_ context.Context, _ string, target domain.VoteTarget, _ int) (*domain.WindowSnapshot, error) {
			snap := &domain.WindowSnapshot{TotalVotes: 1}
			if target == domain.VoteTargetPositive {
				snap.ForRatio = 1.0
			} else {
				snap.AgainstRatio = 1.0
			}
			return snap, nil
		},
	}
	debounce := &mockDebouncer{
		isDebouncedFn: func(_ context.Context, _ string, _ string) (bool, error) {
			return false, nil // not debounced = allowed to vote
		},
	}
	return configSource, sentiment, debounce
}

func TestProcessMessage_Success(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteApplied, result)
	assert.NotNil(t, snap)
	assert.Equal(t, 1.0, snap.ForRatio)
	assert.Equal(t, 1, snap.TotalVotes)
}

func TestProcessMessage_AgainstVote(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "no")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteApplied, result)
	assert.NotNil(t, snap)
	assert.Equal(t, 1.0, snap.AgainstRatio)
}

func TestProcessMessage_NoConfig(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	configSource.getConfigByBroadcasterFn = func(_ context.Context, _ string) (domain.OverlayConfig, error) {
		return domain.OverlayConfig{}, domain.ErrConfigNotFound
	}
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteNoMatch, result)
	assert.Nil(t, snap)
}

func TestProcessMessage_ConfigLookupError(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	configSource.getConfigByBroadcasterFn = func(_ context.Context, _ string) (domain.OverlayConfig, error) {
		return domain.OverlayConfig{}, errors.New("redis error")
	}
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.Error(t, err)
	assert.Equal(t, domain.VoteNoMatch, result)
	assert.Nil(t, snap)
}

func TestProcessMessage_NoTriggerMatch(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "hello world")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteNoMatch, result)
	assert.Nil(t, snap)
}

func TestProcessMessage_Debounced(t *testing.T) {
	configSource, sentiment, debounce := newHappyPathMocks()
	debounce.isDebouncedFn = func(_ context.Context, _ string, _ string) (bool, error) {
		return true, nil // is debounced = cannot vote
	}
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, domain.VoteDebounced, result)
	assert.Nil(t, snap)
}

func TestProcessMessage_RecordVoteError(t *testing.T) {
	configSource, _, debounce := newHappyPathMocks()
	sentiment := &mockSentimentStore{
		recordVoteFn: func(_ context.Context, _ string, _ domain.VoteTarget, _ int) (*domain.WindowSnapshot, error) {
			return nil, errors.New("redis stream error")
		},
	}
	ovl := NewOverlay(configSource, sentiment, debounce)

	snap, result, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "yes")
	assert.Error(t, err)
	assert.Equal(t, domain.VoteNoMatch, result)
	assert.Nil(t, snap)
}

func TestProcessMessage_PassesWindowSeconds(t *testing.T) {
	customConfig := domain.OverlayConfig{
		MemorySeconds:  60,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DisplayMode:    domain.DisplayModeCombined,
	}
	configSource := &mockConfigSource{
		getConfigByBroadcasterFn: func(_ context.Context, _ string) (domain.OverlayConfig, error) {
			return customConfig, nil
		},
	}
	var capturedWindowSeconds int
	sentiment := &mockSentimentStore{
		recordVoteFn: func(_ context.Context, _ string, _ domain.VoteTarget, windowSeconds int) (*domain.WindowSnapshot, error) {
			capturedWindowSeconds = windowSeconds
			return &domain.WindowSnapshot{ForRatio: 1.0, TotalVotes: 1}, nil
		},
	}
	debounce := &mockDebouncer{
		isDebouncedFn: func(_ context.Context, _ string, _ string) (bool, error) {
			return false, nil
		},
	}
	ovl := NewOverlay(configSource, sentiment, debounce)

	_, _, err := ovl.ProcessMessage(context.Background(), "broadcaster-1", "chatter-1", "yes")
	require.NoError(t, err)
	assert.Equal(t, 60, capturedWindowSeconds)
}

// --- matchTrigger Tests ---

func TestMatchTrigger_ForTrigger(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetPositive, matchTrigger("yes", config))
}

func TestMatchTrigger_AgainstTrigger(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetNegative, matchTrigger("no", config))
}

func TestMatchTrigger_NoMatch(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("hello world", config))
}

func TestMatchTrigger_CaseInsensitive(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetPositive, matchTrigger("YES", config))
	assert.Equal(t, domain.VoteTargetNegative, matchTrigger("NO", config))
}

func TestMatchTrigger_ForTriggerPriority(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "vote", AgainstTrigger: "vote"}
	// When both triggers are the same, ForTrigger takes priority
	assert.Equal(t, domain.VoteTargetPositive, matchTrigger("vote", config))
}

func TestMatchTrigger_ZeroConfig(t *testing.T) {
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("yes", domain.OverlayConfig{}))
}

func TestMatchTrigger_EmptyMessage(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("", config))
}

func TestMatchTrigger_EmptyTriggers(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "", AgainstTrigger: ""}
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("hello world", config))
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("yes", config))
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("no", config))
}

func TestMatchTrigger_OneEmptyTrigger(t *testing.T) {
	// Empty for-trigger should not match, but against-trigger should
	config := domain.OverlayConfig{ForTrigger: "", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("hello", config))
	assert.Equal(t, domain.VoteTargetNegative, matchTrigger("no", config))

	// Empty against-trigger should not match, but for-trigger should
	config2 := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: ""}
	assert.Equal(t, domain.VoteTargetPositive, matchTrigger("yes", config2))
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("hello", config2))
}

func TestMatchTrigger_SubstringDoesNotMatch(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("I vote yes!", config))
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("vote no", config))
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("yes please", config))
	assert.Equal(t, domain.VoteTargetNone, matchTrigger("nope", config))
}

func TestMatchTrigger_WhitespaceHandling(t *testing.T) {
	config := domain.OverlayConfig{ForTrigger: "yes", AgainstTrigger: "no"}
	assert.Equal(t, domain.VoteTargetPositive, matchTrigger("  yes  ", config))
	assert.Equal(t, domain.VoteTargetNegative, matchTrigger("\tno\t", config))
	assert.Equal(t, domain.VoteTargetPositive, matchTrigger("  YES  ", config))
}

// --- ParseDisplayMode Tests ---

func TestParseDisplayMode(t *testing.T) {
	assert.Equal(t, domain.DisplayModeCombined, domain.ParseDisplayMode(""))
	assert.Equal(t, domain.DisplayModeCombined, domain.ParseDisplayMode("combined"))
	assert.Equal(t, domain.DisplayModeSplit, domain.ParseDisplayMode("split"))
	assert.Equal(t, domain.DisplayModeCombined, domain.ParseDisplayMode("invalid"))
}
