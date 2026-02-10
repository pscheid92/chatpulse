package redis

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyVote_Basic(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Apply +10 vote (no prior value, no decay)
	newVal, err := runner.ApplyVote(ctx, overlayUUID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 10.0, newVal)

	// Apply another +10 immediately (no time elapsed = no decay)
	newVal, err = runner.ApplyVote(ctx, overlayUUID, 10.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 20.0, newVal)

	// Apply -30 immediately
	newVal, err = runner.ApplyVote(ctx, overlayUUID, -30.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, -10.0, newVal)
}

func TestApplyVote_WithDecay(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Set initial value
	newVal, err := runner.ApplyVote(ctx, overlayUUID, 80.0, 1.0, now)
	require.NoError(t, err)
	assert.Equal(t, 80.0, newVal)

	// Apply vote 1 second later with decay_rate=1.0
	// Decay: 80 * exp(-1.0 * 1.0) ≈ 80 * 0.3679 ≈ 29.43
	// Then add +10 = 39.43
	laterMs := now + 1000
	newVal, err = runner.ApplyVote(ctx, overlayUUID, 10.0, 1.0, laterMs)
	require.NoError(t, err)
	expected := 80.0*math.Exp(-1.0*1.0) + 10.0
	assert.InDelta(t, expected, newVal, 0.01)
}

func TestApplyVote_Clamping(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Push past +100
	for range 15 {
		_, err = runner.ApplyVote(ctx, overlayUUID, 10.0, 0.0, now) // decay_rate=0 means no decay
		require.NoError(t, err)
	}
	val, err := runner.ApplyVote(ctx, overlayUUID, 10.0, 0.0, now)
	require.NoError(t, err)
	assert.Equal(t, 100.0, val)

	// Reset and push past -100
	err = store.UpdateValue(ctx, overlayUUID, 0)
	require.NoError(t, err)
	for range 15 {
		_, err = runner.ApplyVote(ctx, overlayUUID, -10.0, 0.0, now)
		require.NoError(t, err)
	}
	val, err = runner.ApplyVote(ctx, overlayUUID, -10.0, 0.0, now)
	require.NoError(t, err)
	assert.Equal(t, -100.0, val)
}

func TestGetDecayedValue(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now().UnixMilli()

	// Set initial value via vote
	_, err = runner.ApplyVote(ctx, overlayUUID, 80.0, 0.0, now)
	require.NoError(t, err)

	// Read immediately — should be 80.0 (no time elapsed)
	val, err := runner.GetDecayedValue(ctx, overlayUUID, 1.0, now)
	require.NoError(t, err)
	assert.InDelta(t, 80.0, val, 0.01)

	// Read 1 second later — should decay
	// 80 * exp(-1.0 * 1.0) ≈ 29.43
	val, err = runner.GetDecayedValue(ctx, overlayUUID, 1.0, now+1000)
	require.NoError(t, err)
	expected := 80.0 * math.Exp(-1.0*1.0)
	assert.InDelta(t, expected, val, 0.01)

	// Original value should be unchanged (pure read)
	rawVal, exists, err := store.GetValue(ctx, overlayUUID)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 80.0, rawVal)
}
