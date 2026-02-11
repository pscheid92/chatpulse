package redis

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRepo(t *testing.T) (*SessionRepo, *clockwork.FakeClock) {
	t.Helper()
	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	return NewSessionRepo(client, clock), clock
}

// --- Session lifecycle ---

func TestActivateSession(t *testing.T) {
	store, _ := setupTestRepo(t)
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

	// Value starts at zero
	raw, err := store.rdb.HGet(ctx, sessionKey(overlayUUID), "value").Result()
	require.NoError(t, err)
	assert.Equal(t, "0", raw)

	cfg, err := store.GetSessionConfig(ctx, overlayUUID)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "PogChamp", cfg.ForTrigger)
	assert.Equal(t, "BabyRage", cfg.AgainstTrigger)
	assert.Equal(t, 1.0, cfg.DecaySpeed)

	foundUUID, found, err := store.GetSessionByBroadcaster(ctx, broadcasterID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, overlayUUID, foundUUID)
}

func TestActivateSession_ResumeExisting(t *testing.T) {
	store, clock := setupTestRepo(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	broadcasterID := "12345"
	config := domain.ConfigSnapshot{ForTrigger: "PogChamp", AgainstTrigger: "BabyRage", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	clock.Advance(1 * time.Second)
	err = store.MarkDisconnected(ctx, overlayUUID)
	require.NoError(t, err)

	err = store.rdb.HSet(ctx, sessionKey(overlayUUID), "value", "50").Err()
	require.NoError(t, err)

	// Re-activate preserves value and clears disconnect
	err = store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	raw, err := store.rdb.HGet(ctx, sessionKey(overlayUUID), "value").Result()
	require.NoError(t, err)
	val, err := strconv.ParseFloat(raw, 64)
	require.NoError(t, err)
	assert.Equal(t, 50.0, val)
}

func TestDeleteSession(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	broadcasterID := "12345"
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	err = store.DeleteSession(ctx, overlayUUID)
	require.NoError(t, err)

	exists, err := store.SessionExists(ctx, overlayUUID)
	require.NoError(t, err)
	assert.False(t, exists)

	foundUUID, found, err := store.GetSessionByBroadcaster(ctx, broadcasterID)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, uuid.Nil, foundUUID)
}

func TestDeleteSession_NonExistent(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	err := store.DeleteSession(ctx, uuid.New())
	require.NoError(t, err)
}

// --- Session queries ---

func TestGetSessionConfig_NonExistent(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	cfg, err := store.GetSessionConfig(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestGetSessionByBroadcaster_NotFound(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	foundUUID, found, err := store.GetSessionByBroadcaster(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, uuid.Nil, foundUUID)
}

// --- Config ---

func TestUpdateConfig(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "old", AgainstTrigger: "old2", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	newConfig := domain.ConfigSnapshot{ForTrigger: "new", AgainstTrigger: "new2", DecaySpeed: 2.0}
	err = store.UpdateConfig(ctx, overlayUUID, newConfig)
	require.NoError(t, err)

	cfg, err := store.GetSessionConfig(ctx, overlayUUID)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "new", cfg.ForTrigger)
	assert.Equal(t, 2.0, cfg.DecaySpeed)
}

// --- Disconnect / orphan handling ---

func TestMarkDisconnected_AndResume(t *testing.T) {
	store, clock := setupTestRepo(t)
	ctx := context.Background()

	overlayUUID := uuid.New()
	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, overlayUUID, "broadcaster1", config)
	require.NoError(t, err)

	clock.Advance(1 * time.Second)
	err = store.MarkDisconnected(ctx, overlayUUID)
	require.NoError(t, err)

	err = store.ResumeSession(ctx, overlayUUID)
	require.NoError(t, err)
}

func TestListOrphans(t *testing.T) {
	store, clock := setupTestRepo(t)
	ctx := context.Background()

	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	// Active session (no disconnect)
	activeUUID := uuid.New()
	err := store.ActivateSession(ctx, activeUUID, "active", config)
	require.NoError(t, err)

	// Old disconnected session (orphan) — disconnected before the clock advance
	orphanUUID := uuid.New()
	err = store.ActivateSession(ctx, orphanUUID, "orphan", config)
	require.NoError(t, err)
	clock.Advance(1 * time.Second)
	err = store.MarkDisconnected(ctx, orphanUUID)
	require.NoError(t, err)

	clock.Advance(5 * time.Minute)

	// Recently disconnected session — disconnected after the clock advance
	recentUUID := uuid.New()
	err = store.ActivateSession(ctx, recentUUID, "recent", config)
	require.NoError(t, err)
	err = store.MarkDisconnected(ctx, recentUUID)
	require.NoError(t, err)

	orphans, err := store.ListOrphans(ctx, 1*time.Minute)
	require.NoError(t, err)
	assert.Len(t, orphans, 1)
	assert.Equal(t, orphanUUID, orphans[0])
}

func TestListOrphans_NoOrphans(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	config := domain.ConfigSnapshot{ForTrigger: "a", AgainstTrigger: "b", DecaySpeed: 1.0}

	err := store.ActivateSession(ctx, uuid.New(), "broadcaster1", config)
	require.NoError(t, err)

	err = store.ActivateSession(ctx, uuid.New(), "broadcaster2", config)
	require.NoError(t, err)

	orphans, err := store.ListOrphans(ctx, 1*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, orphans)
}

// --- Ref counting ---

func TestRefCount(t *testing.T) {
	store, _ := setupTestRepo(t)
	ctx := context.Background()

	overlayUUID := uuid.New()

	count, err := store.IncrRefCount(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	count, err = store.IncrRefCount(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	count, err = store.DecrRefCount(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}
