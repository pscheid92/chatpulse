package postgres

import (
	"context"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertUser_Insert(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	user, err := repo.Upsert(ctx, "12345", "testuser")

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, user.ID)
	assert.NotEqual(t, uuid.Nil, user.OverlayUUID)
	assert.Equal(t, "12345", user.TwitchUserID)
	assert.Equal(t, "testuser", user.TwitchUsername)

	// Verify default config was created
	config, err := configRepo.GetByStreamerID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.AgainstLabel)
	assert.Equal(t, "For", config.ForLabel)
	assert.Equal(t, 30, config.MemorySeconds)
}

func TestUpsertUser_Update(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	// Insert
	user1, err := repo.Upsert(ctx, "12345", "testuser")
	require.NoError(t, err)

	originalID := user1.ID
	originalOverlayUUID := user1.OverlayUUID

	// Update with same TwitchUserID
	user2, err := repo.Upsert(ctx, "12345", "testuser_renamed")
	require.NoError(t, err)

	// Should have same IDs but updated fields
	assert.Equal(t, originalID, user2.ID)
	assert.Equal(t, originalOverlayUUID, user2.OverlayUUID)
	assert.Equal(t, "testuser_renamed", user2.TwitchUsername)

	// Config should still exist (not duplicated)
	config, err := configRepo.GetByStreamerID(ctx, user2.ID)
	require.NoError(t, err)
	assert.NotNil(t, config)
}

func TestGetUserByID_Success(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	// Insert user
	insertedUser, err := repo.Upsert(ctx, "12345", "testuser")
	require.NoError(t, err)

	// Get user by ID
	user, err := repo.GetByID(ctx, insertedUser.ID)
	require.NoError(t, err)
	assert.Equal(t, insertedUser.ID, user.ID)
	assert.Equal(t, insertedUser.TwitchUserID, user.TwitchUserID)
	assert.Equal(t, insertedUser.TwitchUsername, user.TwitchUsername)
}

func TestGetUserByID_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	user, err := repo.GetByID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrStreamerNotFound)
	assert.Nil(t, user)
}

func TestGetUserByOverlayUUID_Success(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	// Insert user
	insertedUser, err := repo.Upsert(ctx, "12345", "testuser")
	require.NoError(t, err)

	// Get user by overlay UUID
	user, err := repo.GetByOverlayUUID(ctx, insertedUser.OverlayUUID)
	require.NoError(t, err)
	assert.Equal(t, insertedUser.ID, user.ID)
	assert.Equal(t, insertedUser.OverlayUUID, user.OverlayUUID)
	assert.Equal(t, insertedUser.TwitchUserID, user.TwitchUserID)
}

func TestGetUserByOverlayUUID_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	randomUUID := uuid.NewV4()
	user, err := repo.GetByOverlayUUID(ctx, randomUUID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrStreamerNotFound)
	assert.Nil(t, user)
}

func TestRotateOverlayUUID_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	_, err := repo.RotateOverlayUUID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrStreamerNotFound)
}

func TestRotateOverlayUUID(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewStreamerRepo(pool)
	ctx := context.Background()

	// Insert user
	user, err := repo.Upsert(ctx, "12345", "testuser")
	require.NoError(t, err)

	oldOverlayUUID := user.OverlayUUID

	// Rotate UUID
	newOverlayUUID, err := repo.RotateOverlayUUID(ctx, user.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldOverlayUUID, newOverlayUUID)
	assert.NotEqual(t, uuid.Nil, newOverlayUUID)

	// Verify old UUID no longer works
	_, err = repo.GetByOverlayUUID(ctx, oldOverlayUUID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrStreamerNotFound)

	// Verify new UUID works
	userByNewUUID, err := repo.GetByOverlayUUID(ctx, newOverlayUUID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, userByNewUUID.ID)

	// Verify GetByID also returns new UUID
	userByID, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, newOverlayUUID, userByID.OverlayUUID)
}
