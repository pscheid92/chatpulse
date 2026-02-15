package redis

import (
	"context"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRepo(t *testing.T) *SessionRepo {
	t.Helper()
	client := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	return NewSessionRepo(client, clock)
}

// --- Session queries ---

func TestGetSessionConfig_NonExistent(t *testing.T) {
	store := setupTestRepo(t)
	ctx := context.Background()

	cfg, err := store.GetSessionConfig(ctx, uuid.NewV4())
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestGetSessionByBroadcaster_NotFound(t *testing.T) {
	store := setupTestRepo(t)
	ctx := context.Background()

	foundUUID, found, err := store.GetSessionByBroadcaster(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, uuid.Nil, foundUUID)
}
