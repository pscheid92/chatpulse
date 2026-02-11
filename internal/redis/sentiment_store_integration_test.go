package redis

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestSentimentStore(t *testing.T) (*SentimentStore, *SessionRepo) {
	t.Helper()
	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	return NewSentimentStore(client), NewSessionRepo(client, clock)
}

func TestApplyVote(t *testing.T) {
	sentiment, sessions := setupTestSentimentStore(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := sessions.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	newVal, err := sentiment.ApplyVote(ctx, overlayUUID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 10.0, newVal)

	// No time elapsed = no decay
	newVal, err = sentiment.ApplyVote(ctx, overlayUUID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 20.0, newVal)

	newVal, err = sentiment.ApplyVote(ctx, overlayUUID, -30.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, -10.0, newVal)
}

func TestApplyVote_WithDecay(t *testing.T) {
	sentiment, sessions := setupTestSentimentStore(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := sessions.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	newVal, err := sentiment.ApplyVote(ctx, overlayUUID, 80.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 80.0, newVal)

	// 1 second later: 80 * exp(-1.0) + 10 ≈ 39.43
	newVal, err = sentiment.ApplyVote(ctx, overlayUUID, 10.0, 1.0, now+1000)
	require.NoError(t, err)
	assert.InDelta(t, 80.0*math.Exp(-1.0)+10.0, newVal, 0.01)
}

func TestApplyVote_Clamping(t *testing.T) {
	sentiment, sessions := setupTestSentimentStore(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := sessions.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Push past +100
	for range 15 {
		_, err = sentiment.ApplyVote(ctx, overlayUUID, 10.0, 0.0, now)
		require.NoError(t, err)
	}
	val, err := sentiment.ApplyVote(ctx, overlayUUID, 10.0, 0.0, now)
	require.NoError(t, err)
	assert.Equal(t, 100.0, val)

	// Reset and push past -100
	err = sentiment.rdb.HSet(ctx, sessionKey(overlayUUID), "value", "0").Err()
	require.NoError(t, err)
	for range 15 {
		_, err = sentiment.ApplyVote(ctx, overlayUUID, -10.0, 0.0, now)
		require.NoError(t, err)
	}
	val, err = sentiment.ApplyVote(ctx, overlayUUID, -10.0, 0.0, now)
	require.NoError(t, err)
	assert.Equal(t, -100.0, val)
}

func TestGetSentiment(t *testing.T) {
	sentiment, sessions := setupTestSentimentStore(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := sessions.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	_, err = sentiment.ApplyVote(ctx, overlayUUID, 80.0, 0.0, now)
	require.NoError(t, err)

	// Immediate read — no decay
	val, err := sentiment.GetSentiment(ctx, overlayUUID, 1.0, now)
	require.NoError(t, err)
	assert.InDelta(t, 80.0, val, 0.01)

	// 1 second later: 80 * exp(-1.0) ≈ 29.43
	val, err = sentiment.GetSentiment(ctx, overlayUUID, 1.0, now+1000)
	require.NoError(t, err)
	assert.InDelta(t, 80.0*math.Exp(-1.0), val, 0.01)

	// Pure read — stored value unchanged
	raw, err := sentiment.rdb.HGet(ctx, sessionKey(overlayUUID), "value").Result()
	require.NoError(t, err)
	rawVal, err := strconv.ParseFloat(raw, 64)
	require.NoError(t, err)
	assert.Equal(t, 80.0, rawVal)
}
