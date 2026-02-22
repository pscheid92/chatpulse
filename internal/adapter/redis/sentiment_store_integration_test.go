package redis

import (
	"context"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestSentimentStore(t *testing.T) *SentimentStore {
	t.Helper()
	client := setupTestClient(t)
	return NewSentimentStore(client)
}

func TestRecordVote_SinglePositive(t *testing.T) {
	store := setupTestSentimentStore(t)
	ctx := context.Background()

	snap, err := store.RecordVote(ctx, "broadcaster1", domain.VoteTargetPositive, 30)
	require.NoError(t, err)
	assert.Equal(t, 1.0, snap.ForRatio)
	assert.Equal(t, 0.0, snap.AgainstRatio)
	assert.Equal(t, 1, snap.TotalVotes)
}

func TestRecordVote_SingleNegative(t *testing.T) {
	store := setupTestSentimentStore(t)
	ctx := context.Background()

	snap, err := store.RecordVote(ctx, "broadcaster2", domain.VoteTargetNegative, 30)
	require.NoError(t, err)
	assert.Equal(t, 0.0, snap.ForRatio)
	assert.Equal(t, 1.0, snap.AgainstRatio)
	assert.Equal(t, 1, snap.TotalVotes)
}

func TestRecordVote_MixedVotes(t *testing.T) {
	store := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-mixed"

	// 3 positive, 2 negative
	for range 3 {
		_, err := store.RecordVote(ctx, broadcasterID, domain.VoteTargetPositive, 30)
		require.NoError(t, err)
	}
	for range 2 {
		_, err := store.RecordVote(ctx, broadcasterID, domain.VoteTargetNegative, 30)
		require.NoError(t, err)
	}

	snap, err := store.GetSnapshot(ctx, broadcasterID, 30)
	require.NoError(t, err)
	assert.Equal(t, 5, snap.TotalVotes)
	assert.InDelta(t, 0.6, snap.ForRatio, 0.01)
	assert.InDelta(t, 0.4, snap.AgainstRatio, 0.01)
}

func TestGetSnapshot_Empty(t *testing.T) {
	store := setupTestSentimentStore(t)
	ctx := context.Background()

	snap, err := store.GetSnapshot(ctx, "broadcaster-empty", 30)
	require.NoError(t, err)
	assert.Equal(t, 0, snap.TotalVotes)
	assert.Equal(t, 0.0, snap.ForRatio)
	assert.Equal(t, 0.0, snap.AgainstRatio)
}

func TestResetSentiment_DeletesStream(t *testing.T) {
	store := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-reset"

	// Add some votes
	for range 5 {
		_, err := store.RecordVote(ctx, broadcasterID, domain.VoteTargetPositive, 30)
		require.NoError(t, err)
	}

	// Verify votes exist
	snap, err := store.GetSnapshot(ctx, broadcasterID, 30)
	require.NoError(t, err)
	assert.Equal(t, 5, snap.TotalVotes)

	// Reset
	err = store.ResetSentiment(ctx, broadcasterID)
	require.NoError(t, err)

	// Verify empty
	snap, err = store.GetSnapshot(ctx, broadcasterID, 30)
	require.NoError(t, err)
	assert.Equal(t, 0, snap.TotalVotes)
}

func TestRecordVote_StreamKeyHasTTL(t *testing.T) {
	store := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-ttl"
	_, err := store.RecordVote(ctx, broadcasterID, domain.VoteTargetPositive, 30)
	require.NoError(t, err)

	ttl := store.rdb.TTL(ctx, votesKey(broadcasterID)).Val()
	assert.Greater(t, ttl.Seconds(), float64(500), "TTL should be ~600 seconds")
	assert.LessOrEqual(t, ttl.Seconds(), float64(600), "TTL should not exceed 600 seconds")
}
