package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenRefreshError_Revoked(t *testing.T) {
	err := &TokenRefreshError{
		Revoked: true,
		Err:     fmt.Errorf("token was revoked by user"),
	}

	assert.Contains(t, err.Error(), "token revoked:")
	assert.Contains(t, err.Error(), "token was revoked by user")
}

func TestTokenRefreshError_NotRevoked(t *testing.T) {
	err := &TokenRefreshError{
		Revoked: false,
		Err:     fmt.Errorf("network error"),
	}

	assert.Contains(t, err.Error(), "token refresh failed:")
	assert.Contains(t, err.Error(), "network error")
}

func TestRefreshToken_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and headers
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		// Verify form data
		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "test_client", r.FormValue("client_id"))
		assert.Equal(t, "test_secret", r.FormValue("client_secret"))
		assert.Equal(t, "refresh_token", r.FormValue("grant_type"))
		assert.Equal(t, "old_refresh", r.FormValue("refresh_token"))

		// Return successful response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "new_access",
			"refresh_token": "new_refresh",
			"expires_in":    7200,
		})
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	access, refresh, expiresIn, err := tr.refreshToken(ctx, "old_refresh")

	require.NoError(t, err)
	assert.Equal(t, "new_access", access)
	assert.Equal(t, "new_refresh", refresh)
	assert.Equal(t, 7200, expiresIn)
}

func TestRefreshToken_BadRequest(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","message":"Invalid refresh token"}`))
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	access, refresh, expiresIn, err := tr.refreshToken(ctx, "invalid_refresh")

	assert.Error(t, err)
	assert.Empty(t, access)
	assert.Empty(t, refresh)
	assert.Equal(t, 0, expiresIn)

	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.True(t, tokenErr.Revoked, "400 status should indicate revoked token")
}

func TestRefreshToken_Unauthorized(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized","message":"Client credentials are invalid"}`))
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	_, _, _, err := tr.refreshToken(ctx, "bad_refresh")

	require.Error(t, err)
	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.True(t, tokenErr.Revoked, "401 status should indicate revoked token")
}

func TestRefreshToken_ServerError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server_error","message":"Internal server error"}`))
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	_, _, _, err := tr.refreshToken(ctx, "any_refresh")

	require.Error(t, err)
	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.False(t, tokenErr.Revoked, "500 status should not indicate revoked token")
}

func TestRefreshToken_MalformedJSON(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	_, _, _, err := tr.refreshToken(ctx, "any_refresh")

	require.Error(t, err)
	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.Contains(t, err.Error(), "token refresh failed")
}

func TestRefreshToken_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Second) // Longer than the 10s timeout
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	_, _, _, err := tr.refreshToken(ctx, "any_refresh")

	require.Error(t, err)
	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.Contains(t, err.Error(), "token refresh failed")
}

func TestRefreshToken_ContextCanceled(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, _, _, err := tr.refreshToken(ctx, "any_refresh")

	require.Error(t, err)
	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
	assert.Contains(t, err.Error(), "token refresh failed")
}

func TestRefreshToken_EmptyResponse(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			// Missing required fields
		})
	}))
	defer mockServer.Close()

	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     mockServer.URL,
	}

	ctx := context.Background()
	access, refresh, expiresIn, err := tr.refreshToken(ctx, "any_refresh")

	// Should succeed but return empty strings (API contract allows this)
	require.NoError(t, err)
	assert.Empty(t, access)
	assert.Empty(t, refresh)
	assert.Equal(t, 0, expiresIn)
}

func TestRefreshToken_NetworkError(t *testing.T) {
	// Use an invalid URL to trigger network error
	tr := &TokenRefresher{
		clientID:     "test_client",
		clientSecret: "test_secret",
		oauthURL:     "http://invalid-host-that-does-not-exist-12345:9999/oauth/token",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, _, err := tr.refreshToken(ctx, "any_refresh")

	require.Error(t, err)
	var tokenErr *TokenRefreshError
	require.ErrorAs(t, err, &tokenErr)
}
