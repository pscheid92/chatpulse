package database

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateEventSubSubscription(t *testing.T) {
	db := setupTestDB(t)
	repo := NewEventSubRepo(db)
	user := CreateTestUser(t, db, "12345")
	ctx := context.Background()

	err := repo.Create(ctx, user.ID, "broadcaster-1", "sub-1", "conduit-1")
	require.NoError(t, err)

	// Verify it was created
	sub, err := repo.GetByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, sub.UserID)
	assert.Equal(t, "broadcaster-1", sub.BroadcasterUserID)
	assert.Equal(t, "sub-1", sub.SubscriptionID)
	assert.Equal(t, "conduit-1", sub.ConduitID)
	assert.False(t, sub.CreatedAt.IsZero())
}

func TestCreateEventSubSubscription_Upsert(t *testing.T) {
	db := setupTestDB(t)
	repo := NewEventSubRepo(db)
	user := CreateTestUser(t, db, "12345")
	ctx := context.Background()

	// Create initial subscription
	err := repo.Create(ctx, user.ID, "broadcaster-1", "sub-1", "conduit-1")
	require.NoError(t, err)

	// Upsert with new values (same user_id)
	err = repo.Create(ctx, user.ID, "broadcaster-2", "sub-2", "conduit-2")
	require.NoError(t, err)

	// Verify upserted values
	sub, err := repo.GetByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "broadcaster-2", sub.BroadcasterUserID)
	assert.Equal(t, "sub-2", sub.SubscriptionID)
	assert.Equal(t, "conduit-2", sub.ConduitID)
}

func TestGetEventSubSubscription_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewEventSubRepo(db)
	ctx := context.Background()

	randomID := uuid.New()
	sub, err := repo.GetByUserID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrSubscriptionNotFound)
	assert.Nil(t, sub)
}

func TestDeleteEventSubSubscription(t *testing.T) {
	db := setupTestDB(t)
	repo := NewEventSubRepo(db)
	user := CreateTestUser(t, db, "12345")
	ctx := context.Background()

	// Create subscription
	err := repo.Create(ctx, user.ID, "broadcaster-1", "sub-1", "conduit-1")
	require.NoError(t, err)

	// Delete it
	err = repo.Delete(ctx, user.ID)
	require.NoError(t, err)

	// Verify it's gone
	sub, err := repo.GetByUserID(ctx, user.ID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrSubscriptionNotFound)
	assert.Nil(t, sub)
}

func TestDeleteEventSubSubscription_NonExistent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewEventSubRepo(db)
	ctx := context.Background()

	// Deleting a non-existent subscription should not error
	err := repo.Delete(ctx, uuid.New())
	assert.NoError(t, err)
}

func TestListEventSubSubscriptions(t *testing.T) {
	db := setupTestDB(t)
	repo := NewEventSubRepo(db)
	ctx := context.Background()

	// List with no subscriptions
	subs, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, subs)

	// Create two users with subscriptions
	user1 := CreateTestUser(t, db, "user-1")
	user2 := CreateTestUser(t, db, "user-2")

	err = repo.Create(ctx, user1.ID, "broadcaster-1", "sub-1", "conduit-1")
	require.NoError(t, err)
	err = repo.Create(ctx, user2.ID, "broadcaster-2", "sub-2", "conduit-2")
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
	assert.True(t, broadcasters["broadcaster-1"])
	assert.True(t, broadcasters["broadcaster-2"])
}
