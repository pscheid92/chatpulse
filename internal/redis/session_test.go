package redis

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivateSession_NewSession(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	broadcasterID := "12345"
	config := domain.ConfigSnapshot{
		ForTrigger:     "PogChamp",
		AgainstTrigger: "BabyRage",
		LeftLabel:      "Negative",
		RightLabel:     "Positive",
		DecaySpeed:     1.0,
	}

	err := store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	// Verify session exists with correct values
	value, cfg, err := store.GetSession(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, 0.0, value)
	require.NotNil(t, cfg)
	assert.Equal(t, "PogChamp", cfg.ForTrigger)
	assert.Equal(t, "BabyRage", cfg.AgainstTrigger)
	assert.Equal(t, 1.0, cfg.DecaySpeed)

	// Verify broadcaster mapping
	foundUUID, err := store.GetSessionByBroadcaster(ctx, broadcasterID)
	require.NoError(t, err)
	assert.Equal(t, overlayUUID, foundUUID)
}

func TestActivateSession_ResumeExisting(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	broadcasterID := "12345"
	config := domain.ConfigSnapshot{ForTrigger: "PogChamp", AgainstTrigger: "BabyRage", DecaySpeed: 1.0}

	// Create session
	err := store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	// Mark disconnected
	err = store.MarkDisconnected(ctx, overlayUUID, time.Now())
	require.NoError(t, err)

	// Update value
	err = store.UpdateValue(ctx, overlayUUID, 50.0)
	require.NoError(t, err)

	// Re-activate (resume)
	err = store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	// Value should be preserved, disconnect cleared
	value, _, err := store.GetSession(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, 50.0, value)
}

func TestGetSession_NonExistent(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	value, cfg, err := store.GetSession(ctx, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 0.0, value)
	assert.Nil(t, cfg)
}

func TestDeleteSession(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	broadcasterID := "12345"
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	err = store.DeleteSession(ctx, overlayUUID)
	require.NoError(t, err)

	// Session gone
	_, cfg, err := store.GetSession(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Nil(t, cfg)

	// Broadcaster mapping gone
	foundUUID, err := store.GetSessionByBroadcaster(ctx, broadcasterID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, foundUUID)
}

func TestGetSessionByBroadcaster_NotFound(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	foundUUID, err := store.GetSessionByBroadcaster(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, foundUUID)
}

func TestCheckAndSetDebounce(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	userID := "user123"

	// First vote: allowed
	allowed, err := store.CheckAndSetDebounce(ctx, overlayUUID, userID)
	require.NoError(t, err)
	assert.True(t, allowed)

	// Second vote immediately: debounced
	allowed, err = store.CheckAndSetDebounce(ctx, overlayUUID, userID)
	require.NoError(t, err)
	assert.False(t, allowed)

	// Different user: allowed
	allowed, err = store.CheckAndSetDebounce(ctx, overlayUUID, "otheruser")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestUpdateAndGetValue(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	err = store.UpdateValue(ctx, overlayUUID, 75.5)
	require.NoError(t, err)

	value, exists, err := store.GetValue(ctx, overlayUUID)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 75.5, value)
}

func TestGetValue_NonExistent(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	_, exists, err := store.GetValue(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMarkAndClearDisconnected(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	now := time.Now()
	err = store.MarkDisconnected(ctx, overlayUUID, now)
	require.NoError(t, err)

	err = store.ClearDisconnected(ctx, overlayUUID)
	require.NoError(t, err)
}

func TestUpdateConfig(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "old", AgainstTrigger: "old2", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	newConfig := domain.ConfigSnapshot{ForTrigger: "new", AgainstTrigger: "new2", DecaySpeed: 2.0}
	err = store.UpdateConfig(ctx, overlayUUID, newConfig)
	require.NoError(t, err)

	_, cfg, err := store.GetSession(ctx, overlayUUID)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "new", cfg.ForTrigger)
	assert.Equal(t, 2.0, cfg.DecaySpeed)
}

func TestListOrphanSessions(t *testing.T) {
	client := setupTestClient(t)
	store := NewSessionStore(client)
	ctx := context.Background()

	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	// Active session (no disconnect)
	activeUUID := uuid.New()
	err := store.ActivateSession(ctx, activeUUID, "active", config)
	require.NoError(t, err)

	// Recently disconnected session
	recentUUID := uuid.New()
	err = store.ActivateSession(ctx, recentUUID, "recent", config)
	require.NoError(t, err)
	err = store.MarkDisconnected(ctx, recentUUID, time.Now())
	require.NoError(t, err)

	// Old disconnected session (orphan)
	orphanUUID := uuid.New()
	err = store.ActivateSession(ctx, orphanUUID, "orphan", config)
	require.NoError(t, err)
	err = store.MarkDisconnected(ctx, orphanUUID, time.Now().Add(-5*time.Minute))
	require.NoError(t, err)

	orphans, err := store.ListOrphanSessions(ctx, 1*time.Minute)
	require.NoError(t, err)
	assert.Len(t, orphans, 1)
	assert.Equal(t, orphanUUID, orphans[0])
}
