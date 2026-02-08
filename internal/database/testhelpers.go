package database

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/stretchr/testify/require"
)

// CreateTestUser is a helper that creates a user with default values for testing.
// Returns the created user.
func CreateTestUser(t *testing.T, db *DB, twitchUserID string) *models.User {
	t.Helper()

	ctx := context.Background()
	expiry := time.Now().UTC().Add(1 * time.Hour)

	user, err := db.UpsertUser(ctx, twitchUserID, "testuser_"+twitchUserID, "access_token", "refresh_token", expiry)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, user.ID)

	return user
}

// CreateTestUserWithTokens creates a user with specific tokens for testing.
func CreateTestUserWithTokens(t *testing.T, db *DB, twitchUserID, username, accessToken, refreshToken string) *models.User {
	t.Helper()

	ctx := context.Background()
	expiry := time.Now().UTC().Add(1 * time.Hour)

	user, err := db.UpsertUser(ctx, twitchUserID, username, accessToken, refreshToken, expiry)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, user.ID)

	return user
}

// UpdateTestConfig is a helper that updates a user's config for testing.
func UpdateTestConfig(t *testing.T, db *DB, userID uuid.UUID, triggerFor, triggerAgainst, labelFor, labelAgainst string, decaySpeed float64) {
	t.Helper()

	ctx := context.Background()
	err := db.UpdateConfig(ctx, userID, triggerFor, triggerAgainst, labelFor, labelAgainst, decaySpeed)
	require.NoError(t, err)
}
