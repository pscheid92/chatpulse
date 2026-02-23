package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestUser(t *testing.T, pool *pgxpool.Pool, twitchUserID string) *domain.Streamer {
	t.Helper()

	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	user, err := repo.Upsert(ctx, twitchUserID, "testuser_"+twitchUserID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, user.ID)

	return user
}

func TestCreateEventSubSubscription(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewEventSubRepo(pool)
	user := createTestUser(t, pool, "12345")
	ctx := context.Background()

	err := repo.Create(ctx, user.ID, "sub-1", "conduit-1")
	require.NoError(t, err)

	// Verify it was created
	sub, err := repo.GetByStreamerID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, sub.StreamerID)
	assert.Equal(t, "12345", sub.BroadcasterUserID) // joined from streamers.twitch_user_id
	assert.Equal(t, "sub-1", sub.SubscriptionID)
	assert.Equal(t, "conduit-1", sub.ConduitID)
	assert.False(t, sub.CreatedAt.IsZero())
}

func TestCreateEventSubSubscription_Upsert(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewEventSubRepo(pool)
	user := createTestUser(t, pool, "12345")
	ctx := context.Background()

	// Create initial subscription
	err := repo.Create(ctx, user.ID, "sub-1", "conduit-1")
	require.NoError(t, err)

	// Upsert with new values (same streamer_id)
	err = repo.Create(ctx, user.ID, "sub-2", "conduit-2")
	require.NoError(t, err)

	// Verify upserted values
	sub, err := repo.GetByStreamerID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "12345", sub.BroadcasterUserID) // still from streamers.twitch_user_id
	assert.Equal(t, "sub-2", sub.SubscriptionID)
	assert.Equal(t, "conduit-2", sub.ConduitID)
}

func TestGetEventSubSubscription_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewEventSubRepo(pool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	sub, err := repo.GetByStreamerID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrSubscriptionNotFound)
	assert.Nil(t, sub)
}

func TestDeleteEventSubSubscription(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewEventSubRepo(pool)
	user := createTestUser(t, pool, "12345")
	ctx := context.Background()

	// Create subscription
	err := repo.Create(ctx, user.ID, "sub-1", "conduit-1")
	require.NoError(t, err)

	// Delete it
	err = repo.Delete(ctx, user.ID)
	require.NoError(t, err)

	// Verify it's gone
	sub, err := repo.GetByStreamerID(ctx, user.ID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrSubscriptionNotFound)
	assert.Nil(t, sub)
}

func TestDeleteEventSubSubscription_NonExistent(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewEventSubRepo(pool)
	ctx := context.Background()

	// Deleting a non-existent subscription should not error
	err := repo.Delete(ctx, uuid.NewV4())
	assert.NoError(t, err)
}

func TestListEventSubSubscriptions(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewEventSubRepo(pool)
	ctx := context.Background()

	// List with no subscriptions
	subs, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, subs)

	// Create two users with subscriptions
	user1 := createTestUser(t, pool, "user-1")
	user2 := createTestUser(t, pool, "user-2")

	err = repo.Create(ctx, user1.ID, "sub-1", "conduit-1")
	require.NoError(t, err)
	err = repo.Create(ctx, user2.ID, "sub-2", "conduit-2")
	require.NoError(t, err)

	// List all
	subs, err = repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, subs, 2)

	// Verify both subscriptions are present (order not guaranteed)
	broadcasters := map[string]bool{}
	for _, sub := range subs {
		broadcasters[sub.BroadcasterUserID] = true
	}
	assert.True(t, broadcasters["user-1"]) // twitch_user_id from streamers table
	assert.True(t, broadcasters["user-2"])
}
