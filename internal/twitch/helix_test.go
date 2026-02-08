package twitch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/twitch-tow/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTokenRefresher implements a simple mock for TokenRefresher
type mockTokenRefresher struct {
	user *models.User
	err  error
}

func (m *mockTokenRefresher) EnsureValidToken(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.user, nil
}

func TestHelixClient_CreateEventSubSubscription_TokenRefreshError(t *testing.T) {
	hc := &HelixClient{
		tokenRefresher: &mockTokenRefresher{
			err: &TokenRefreshError{
				Revoked: true,
				Err:     fmt.Errorf("token revoked"),
			},
		},
	}

	ctx := context.Background()
	userID := uuid.New()

	subscriptionID, err := hc.CreateEventSubSubscription(ctx, userID, "channel.chat.message", "12345", "session123")

	assert.Error(t, err)
	assert.Empty(t, subscriptionID)

	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.True(t, tokenErr.Revoked)
}

func TestHelixClient_DeleteEventSubSubscription_TokenRefreshError(t *testing.T) {
	hc := &HelixClient{
		tokenRefresher: &mockTokenRefresher{
			err: fmt.Errorf("database connection failed"),
		},
	}

	ctx := context.Background()
	userID := uuid.New()

	err := hc.DeleteEventSubSubscription(ctx, userID, "sub123")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database connection failed")
}

// Note: Full integration tests for helix API calls would require either:
// 1. Mocking the helix.Client (requires interface extraction)
// 2. Setting up a mock Twitch API server
// 3. Using the real Twitch API (not suitable for unit tests)
//
// The current tests verify error handling and token refresh integration.
// The helix library itself is well-tested upstream.

func TestMockTokenRefresher_Success(t *testing.T) {
	expectedUser := &models.User{
		ID:           uuid.New(),
		TwitchUserID: "12345",
		AccessToken:  "test_token",
		TokenExpiry:  time.Now().UTC().Add(1 * time.Hour),
	}

	mock := &mockTokenRefresher{user: expectedUser}

	ctx := context.Background()
	user, err := mock.EnsureValidToken(ctx, expectedUser.ID)

	require.NoError(t, err)
	assert.Equal(t, expectedUser, user)
}

func TestMockTokenRefresher_Error(t *testing.T) {
	expectedErr := fmt.Errorf("mock error")
	mock := &mockTokenRefresher{err: expectedErr}

	ctx := context.Background()
	user, err := mock.EnsureValidToken(ctx, uuid.New())

	assert.Error(t, err)
	assert.Nil(t, user)
	assert.Equal(t, expectedErr, err)
}
