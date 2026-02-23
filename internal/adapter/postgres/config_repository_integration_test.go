package postgres

import (
	"context"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetConfig(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewStreamerRepo(pool)
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	// Insert user (creates default config)
	user, err := userRepo.Upsert(ctx, "12345", "testuser")
	require.NoError(t, err)

	// Get config
	config, err := configRepo.GetByStreamerID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.AgainstLabel)
	assert.Equal(t, "For", config.ForLabel)
	assert.Equal(t, 30, config.MemorySeconds)
}

func TestGetConfig_NotFound(t *testing.T) {
	_ = setupTestDB(t)
	configRepo := NewConfigRepo(testPool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	config, err := configRepo.GetByStreamerID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConfigNotFound)
	assert.Nil(t, config)
}

func TestUpdateConfig_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	err := configRepo.Update(ctx, randomID, domain.OverlayConfig{
		MemorySeconds:  45,
		ForTrigger:     "LUL",
		ForLabel:       "Sad",
		AgainstTrigger: "BibleThump",
		AgainstLabel:   "Happy",
	}, 2)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConfigNotFound)
}

func TestUpdateConfig(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewStreamerRepo(pool)
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	// Insert user
	user, err := userRepo.Upsert(ctx, "12345", "testuser")
	require.NoError(t, err)

	// Update config
	err = configRepo.Update(ctx, user.ID, domain.OverlayConfig{
		ForTrigger:     "LUL",
		AgainstTrigger: "BibleThump",
		AgainstLabel:   "Happy",
		ForLabel:       "Sad",
		MemorySeconds:  60,
	}, 2)
	require.NoError(t, err)

	// Verify update
	config, err := configRepo.GetByStreamerID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "LUL", config.ForTrigger)
	assert.Equal(t, "BibleThump", config.AgainstTrigger)
	assert.Equal(t, "Happy", config.AgainstLabel)
	assert.Equal(t, "Sad", config.ForLabel)
	assert.Equal(t, 60, config.MemorySeconds)
}
