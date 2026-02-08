package redis

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyVote_Basic(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := models.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	// Apply +10 vote
	newVal, err := runner.ApplyVote(ctx, overlayUUID, 10.0)
	require.NoError(t, err)
	assert.Equal(t, 10.0, newVal)

	// Apply another +10
	newVal, err = runner.ApplyVote(ctx, overlayUUID, 10.0)
	require.NoError(t, err)
	assert.Equal(t, 20.0, newVal)

	// Apply -30
	newVal, err = runner.ApplyVote(ctx, overlayUUID, -30.0)
	require.NoError(t, err)
	assert.Equal(t, -10.0, newVal)
}

func TestApplyVote_Clamping(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := models.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	// Push past +100
	for i := 0; i < 15; i++ {
		_, err = runner.ApplyVote(ctx, overlayUUID, 10.0)
		require.NoError(t, err)
	}
	val, err := runner.ApplyVote(ctx, overlayUUID, 10.0)
	require.NoError(t, err)
	assert.Equal(t, 100.0, val)

	// Reset and push past -100
	err = store.UpdateValue(ctx, overlayUUID, 0)
	require.NoError(t, err)
	for i := 0; i < 15; i++ {
		_, err = runner.ApplyVote(ctx, overlayUUID, -10.0)
		require.NoError(t, err)
	}
	val, err = runner.ApplyVote(ctx, overlayUUID, -10.0)
	require.NoError(t, err)
	assert.Equal(t, -100.0, val)
}

func TestApplyDecay(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	runner := NewScriptRunner(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := models.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}
	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	// Set initial value
	err = store.UpdateValue(ctx, overlayUUID, 80.0)
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	decayFactor := 0.95
	minInterval := int64(40)

	// First decay should apply
	newVal, err := runner.ApplyDecay(ctx, overlayUUID, decayFactor, now, minInterval)
	require.NoError(t, err)
	assert.InDelta(t, 76.0, newVal, 0.01) // 80 * 0.95

	// Immediate second decay should be skipped (within minInterval)
	newVal2, err := runner.ApplyDecay(ctx, overlayUUID, decayFactor, now+10, minInterval)
	require.NoError(t, err)
	assert.InDelta(t, 76.0, newVal2, 0.01) // unchanged

	// Decay after interval should apply
	newVal3, err := runner.ApplyDecay(ctx, overlayUUID, decayFactor, now+50, minInterval)
	require.NoError(t, err)
	assert.InDelta(t, 72.2, newVal3, 0.01) // 76 * 0.95
}
