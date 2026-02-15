package database

import (
	"context"
	"testing"
	"time"

	"github.com/pscheid92/chatpulse/internal/crypto"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetConfig(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool, crypto.NoopService{})
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	// Insert user (creates default config)
	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := userRepo.Upsert(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Get config
	config, err := configRepo.GetByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, config.UserID)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.LeftLabel)
	assert.Equal(t, "For", config.RightLabel)
	assert.InDelta(t, 0.5, config.DecaySpeed, 0.01)
}

func TestGetConfig_NotFound(t *testing.T) {
	_ = setupTestDB(t)
	configRepo := NewConfigRepo(testPool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	config, err := configRepo.GetByUserID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConfigNotFound)
	assert.Nil(t, config)
}

func TestUpdateConfig_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	randomID := uuid.NewV4()
	err := configRepo.Update(ctx, randomID, "LUL", "BibleThump", "Happy", "Sad", 1.5, 2)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConfigNotFound)
}

func TestUpdateConfig(t *testing.T) {
	pool := setupTestDB(t)
	userRepo := NewUserRepo(pool, crypto.NoopService{})
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := userRepo.Upsert(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Update config
	err = configRepo.Update(ctx, user.ID, "LUL", "BibleThump", "Happy", "Sad", 1.5, 2)
	require.NoError(t, err)

	// Verify update
	config, err := configRepo.GetByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "LUL", config.ForTrigger)
	assert.Equal(t, "BibleThump", config.AgainstTrigger)
	assert.Equal(t, "Happy", config.LeftLabel)
	assert.Equal(t, "Sad", config.RightLabel)
	assert.InDelta(t, 1.5, config.DecaySpeed, 0.01)
}
