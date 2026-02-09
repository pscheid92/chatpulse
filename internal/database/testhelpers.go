package database

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/require"
)

// CreateTestUser is a helper that creates a user with default values for testing.
// Returns the created user.
func CreateTestUser(t *testing.T, db *DB, twitchUserID string) *domain.User {
	t.Helper()

	repo := NewUserRepo(db)
	ctx := context.Background()
	expiry := time.Now().UTC().Add(1 * time.Hour)

	user, err := repo.Upsert(ctx, twitchUserID, "testuser_"+twitchUserID, "access_token", "refresh_token", expiry)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, user.ID)

	return user
}

// CreateTestUserWithTokens creates a user with specific tokens for testing.
func CreateTestUserWithTokens(t *testing.T, db *DB, twitchUserID, username, accessToken, refreshToken string) *domain.User {
	t.Helper()

	repo := NewUserRepo(db)
	ctx := context.Background()
	expiry := time.Now().UTC().Add(1 * time.Hour)

	user, err := repo.Upsert(ctx, twitchUserID, username, accessToken, refreshToken, expiry)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, user.ID)

	return user
}

// UpdateTestConfig is a helper that updates a user's config for testing.
func UpdateTestConfig(t *testing.T, db *DB, userID uuid.UUID, triggerFor, triggerAgainst, labelFor, labelAgainst string, decaySpeed float64) {
	t.Helper()

	repo := NewConfigRepo(db)
	ctx := context.Background()
	err := repo.Update(ctx, userID, triggerFor, triggerAgainst, labelFor, labelAgainst, decaySpeed)
	require.NoError(t, err)
}
