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

func TestUpsertUser_Insert(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := repo.Upsert(ctx, "12345", "testuser", "access_token", "refresh_token", expiry)

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, user.ID)
	assert.NotEqual(t, uuid.Nil, user.OverlayUUID)
	assert.Equal(t, "12345", user.TwitchUserID)
	assert.Equal(t, "testuser", user.TwitchUsername)
	// Compare times in UTC to avoid timezone issues
	assert.WithinDuration(t, expiry, user.TokenExpiry, time.Second)

	// Verify default config was created
	config, err := configRepo.GetByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, config.UserID)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.LeftLabel)
	assert.Equal(t, "For", config.RightLabel)
	assert.InDelta(t, 0.5, config.DecaySpeed, 0.01)
}

func TestUpsertUser_Update(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	configRepo := NewConfigRepo(pool)
	ctx := context.Background()

	// Insert
	expiry1 := time.Now().UTC().Add(1 * time.Hour)
	user1, err := repo.Upsert(ctx, "12345", "testuser", "access1", "refresh1", expiry1)
	require.NoError(t, err)

	originalID := user1.ID
	originalOverlayUUID := user1.OverlayUUID

	// Update with same TwitchUserID
	expiry2 := time.Now().UTC().Add(2 * time.Hour)
	user2, err := repo.Upsert(ctx, "12345", "testuser_renamed", "access2", "refresh2", expiry2)
	require.NoError(t, err)

	// Should have same IDs but updated fields
	assert.Equal(t, originalID, user2.ID)
	assert.Equal(t, originalOverlayUUID, user2.OverlayUUID)
	assert.Equal(t, "testuser_renamed", user2.TwitchUsername)
	assert.WithinDuration(t, expiry2, user2.TokenExpiry, time.Second)

	// Config should still exist (not duplicated)
	config, err := configRepo.GetByUserID(ctx, user2.ID)
	require.NoError(t, err)
	assert.Equal(t, user2.ID, config.UserID)
}

func TestUpsertUser_TokenEncryption(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	cryptoSvc, err := crypto.NewAesGcmCryptoService(testEncryptionKey)
	require.NoError(t, err)

	repo := NewUserRepo(pool, cryptoSvc)

	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := repo.Upsert(ctx, "12345", "testuser", "plaintext_access", "plaintext_refresh", expiry)
	require.NoError(t, err)

	// Query raw tokens from database
	var rawAccess, rawRefresh string
	err = pool.QueryRow(ctx, "SELECT access_token, refresh_token FROM users WHERE id = $1", user.ID).Scan(&rawAccess, &rawRefresh)
	require.NoError(t, err)

	// Tokens should be encrypted (not equal to plaintext)
	assert.NotEqual(t, "plaintext_access", rawAccess)
	assert.NotEqual(t, "plaintext_refresh", rawRefresh)

	// User object should have decrypted tokens
	assert.Equal(t, "plaintext_access", user.AccessToken)
	assert.Equal(t, "plaintext_refresh", user.RefreshToken)
}

func TestGetUserByID_Success(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	insertedUser, err := repo.Upsert(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Get user by ID
	user, err := repo.GetByID(ctx, insertedUser.ID)
	require.NoError(t, err)
	assert.Equal(t, insertedUser.ID, user.ID)
	assert.Equal(t, insertedUser.TwitchUserID, user.TwitchUserID)
	assert.Equal(t, insertedUser.TwitchUsername, user.TwitchUsername)
	assert.Equal(t, "access", user.AccessToken)
	assert.Equal(t, "refresh", user.RefreshToken)
	assert.WithinDuration(t, expiry, user.TokenExpiry, time.Second)
}

func TestGetUserByID_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	randomID := uuid.NewV4()
	user, err := repo.GetByID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserNotFound)
	assert.Nil(t, user)
}

func TestGetUserByID_TokenDecryption(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	cryptoSvc, err := crypto.NewAesGcmCryptoService(testEncryptionKey)
	require.NoError(t, err)

	repo := NewUserRepo(pool, cryptoSvc)

	expiry := time.Now().UTC().Add(1 * time.Hour)
	insertedUser, err := repo.Upsert(ctx, "12345", "testuser", "plain_access", "plain_refresh", expiry)
	require.NoError(t, err)

	// Get user - tokens should be decrypted
	user, err := repo.GetByID(ctx, insertedUser.ID)
	require.NoError(t, err)
	assert.Equal(t, "plain_access", user.AccessToken)
	assert.Equal(t, "plain_refresh", user.RefreshToken)
}

func TestGetUserByOverlayUUID_Success(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	insertedUser, err := repo.Upsert(ctx, "12345", "testuser", "access", "refresh", expiry)
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
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	randomUUID := uuid.NewV4()
	user, err := repo.GetByOverlayUUID(ctx, randomUUID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserNotFound)
	assert.Nil(t, user)
}

func TestUpdateUserTokens(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	// Insert user
	expiry1 := time.Now().UTC().Add(1 * time.Hour)
	user, err := repo.Upsert(ctx, "12345", "testuser", "old_access", "old_refresh", expiry1)
	require.NoError(t, err)

	// Update tokens
	expiry2 := time.Now().UTC().Add(2 * time.Hour)
	err = repo.UpdateTokens(ctx, user.ID, "new_access", "new_refresh", expiry2)
	require.NoError(t, err)

	// Verify update
	updatedUser, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new_access", updatedUser.AccessToken)
	assert.Equal(t, "new_refresh", updatedUser.RefreshToken)
	assert.WithinDuration(t, expiry2, updatedUser.TokenExpiry, time.Second)
}

func TestRotateOverlayUUID_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	randomID := uuid.NewV4()
	_, err := repo.RotateOverlayUUID(ctx, randomID)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserNotFound)
}

func TestUpdateTokens_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	randomID := uuid.NewV4()
	expiry := time.Now().UTC().Add(1 * time.Hour)
	err := repo.UpdateTokens(ctx, randomID, "access", "refresh", expiry)

	assert.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserNotFound)
}

func TestGetUserByID_EncryptionKeyMismatch(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Write tokens with key A
	keyA := testEncryptionKey
	cryptoA, err := crypto.NewAesGcmCryptoService(keyA)
	require.NoError(t, err)
	repoA := NewUserRepo(pool, cryptoA)

	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := repoA.Upsert(ctx, "12345", "testuser", "secret_access", "secret_refresh", expiry)
	require.NoError(t, err)

	// Read tokens with key B
	keyB := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	cryptoB, err := crypto.NewAesGcmCryptoService(keyB)
	require.NoError(t, err)
	repoB := NewUserRepo(pool, cryptoB)

	_, err = repoB.GetByID(ctx, user.ID)
	assert.Error(t, err)
}

func TestRotateOverlayUUID(t *testing.T) {
	pool := setupTestDB(t)
	repo := NewUserRepo(pool, crypto.NoopService{})
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := repo.Upsert(ctx, "12345", "testuser", "access", "refresh", expiry)
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
	assert.ErrorIs(t, err, domain.ErrUserNotFound)

	// Verify new UUID works
	userByNewUUID, err := repo.GetByOverlayUUID(ctx, newOverlayUUID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, userByNewUUID.ID)

	// Verify GetByID also returns new UUID
	userByID, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, newOverlayUUID, userByID.OverlayUUID)
}
