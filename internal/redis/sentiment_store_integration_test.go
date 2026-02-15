package redis

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestSentimentStore(t *testing.T) *SentimentStore {
	t.Helper()
	client := setupTestClient(t)
	return NewSentimentStore(client)
}

func TestApplyVote(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster1"
	now := time.Now().UnixMilli()

	newVal, err := sentiment.ApplyVote(ctx, broadcasterID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 10.0, newVal)

	// No time elapsed = no decay
	newVal, err = sentiment.ApplyVote(ctx, broadcasterID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 20.0, newVal)

	newVal, err = sentiment.ApplyVote(ctx, broadcasterID, -30.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, -10.0, newVal)
}

func TestApplyVote_WithDecay(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-decay"
	now := time.Now().UnixMilli()

	newVal, err := sentiment.ApplyVote(ctx, broadcasterID, 80.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 80.0, newVal)

	// 1 second later: 80 * exp(-1.0) + 10 ≈ 39.43
	newVal, err = sentiment.ApplyVote(ctx, broadcasterID, 10.0, 1.0, now+1000)
	require.NoError(t, err)
	assert.InDelta(t, 80.0*math.Exp(-1.0)+10.0, newVal, 0.01)
}

func TestApplyVote_Clamping(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-clamp"
	now := time.Now().UnixMilli()

	// Push past +100
	for range 15 {
		_, err := sentiment.ApplyVote(ctx, broadcasterID, 10.0, 0.0, now)
		require.NoError(t, err)
	}
	val, err := sentiment.ApplyVote(ctx, broadcasterID, 10.0, 0.0, now)
	require.NoError(t, err)
	assert.Equal(t, 100.0, val)

	// Reset and push past -100
	err = sentiment.rdb.HSet(ctx, sentimentKey(broadcasterID), "value", "0").Err()
	require.NoError(t, err)
	for range 15 {
		_, err = sentiment.ApplyVote(ctx, broadcasterID, -10.0, 0.0, now)
		require.NoError(t, err)
	}
	val, err = sentiment.ApplyVote(ctx, broadcasterID, -10.0, 0.0, now)
	require.NoError(t, err)
	assert.Equal(t, -100.0, val)
}

func TestApplyVote_InvalidRedisValue(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-invalid"

	// Corrupt the stored value with non-numeric data
	err := sentiment.rdb.HSet(ctx, sentimentKey(broadcasterID), "value", "not-a-number").Err()
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// ApplyVote should gracefully degrade (use default 0) when Redis value is corrupt
	// The Lua validate_number function returns default=0 for non-numeric values
	value, err := sentiment.ApplyVote(ctx, broadcasterID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 10.0, value, "Should apply delta to default value (0)") // 0 + 10 = 10
}

func TestApplyVote_TTLRefreshed(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-ttl"
	now := time.Now().UnixMilli()

	// Apply a vote to create the key
	_, err := sentiment.ApplyVote(ctx, broadcasterID, 10.0, 1.0, now)
	require.NoError(t, err)

	// Verify TTL is set to ~600 seconds (10 minutes)
	ttl := sentiment.rdb.TTL(ctx, sentimentKey(broadcasterID)).Val()
	assert.Greater(t, ttl.Seconds(), float64(590), "TTL should be ~600 seconds")
	assert.LessOrEqual(t, ttl.Seconds(), float64(600), "TTL should not exceed 600 seconds")
}

func TestApplyVote_PublishesChange(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-publish"

	// Subscribe to the sentiment:changes channel
	sub := sentiment.rdb.Subscribe(ctx, "sentiment:changes")
	defer func() { _ = sub.Close() }()

	// Wait for subscription to be ready
	_, err := sub.Receive(ctx)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Apply a vote
	_, err = sentiment.ApplyVote(ctx, broadcasterID, 10.0, 1.0, now)
	require.NoError(t, err)

	// Receive the published message
	msg, err := sub.ReceiveMessage(ctx)
	require.NoError(t, err)

	assert.Equal(t, "sentiment:changes", msg.Channel)
	assert.Contains(t, msg.Payload, broadcasterID)
	assert.Contains(t, msg.Payload, "broadcaster_id")
	assert.Contains(t, msg.Payload, "value")
	assert.Contains(t, msg.Payload, "timestamp")
}

func TestResetSentiment_DeletesKey(t *testing.T) {
	sentiment := setupTestSentimentStore(t)
	ctx := context.Background()

	broadcasterID := "broadcaster-reset"
	now := time.Now().UnixMilli()

	// Apply a vote to create the key
	_, err := sentiment.ApplyVote(ctx, broadcasterID, 50.0, 1.0, now)
	require.NoError(t, err)

	// Verify key exists
	exists, err := sentiment.rdb.Exists(ctx, sentimentKey(broadcasterID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)

	// Reset sentiment
	err = sentiment.ResetSentiment(ctx, broadcasterID)
	require.NoError(t, err)

	// Verify key is deleted
	exists, err = sentiment.rdb.Exists(ctx, sentimentKey(broadcasterID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists)
}
