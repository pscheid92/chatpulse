package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/twitch-tow/internal/database"
	"github.com/pscheid92/twitch-tow/internal/models"
)

type TokenRefreshError struct {
	Revoked bool
	Err     error
}

func (e *TokenRefreshError) Error() string {
	if e.Revoked {
		return fmt.Sprintf("token revoked: %v", e.Err)
	}
	return fmt.Sprintf("token refresh failed: %v", e.Err)
}

type TokenRefresher struct {
	db           *database.DB
	clientID     string
	clientSecret string
	oauthURL     string // OAuth token endpoint URL (configurable for testing)
}

func NewTokenRefresher(db *database.DB, clientID, clientSecret string) *TokenRefresher {
	return &TokenRefresher{
		db:           db,
		clientID:     clientID,
		clientSecret: clientSecret,
		oauthURL:     "https://id.twitch.tv/oauth2/token", // Default to Twitch
	}
}

func (tr *TokenRefresher) EnsureValidToken(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	user, err := tr.db.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	// Check if token needs refresh (expires in less than 60 seconds)
	if time.Now().Add(60 * time.Second).Before(user.TokenExpiry) {
		return user, nil
	}

	// Token needs refresh
	newAccessToken, newRefreshToken, expiresIn, err := tr.refreshToken(ctx, user.RefreshToken)
	if err != nil {
		return nil, err
	}

	tokenExpiry := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if err := tr.db.UpdateUserTokens(ctx, userID, newAccessToken, newRefreshToken, tokenExpiry); err != nil {
		return nil, fmt.Errorf("failed to update tokens: %w", err)
	}

	user.AccessToken = newAccessToken
	user.RefreshToken = newRefreshToken
	user.TokenExpiry = tokenExpiry

	return user, nil
}

func (tr *TokenRefresher) refreshToken(ctx context.Context, refreshToken string) (accessToken, newRefreshToken string, expiresIn int, err error) {
	data := url.Values{}
	data.Set("client_id", tr.clientID)
	data.Set("client_secret", tr.clientSecret)
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, "POST", tr.oauthURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", 0, &TokenRefreshError{Err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, &TokenRefreshError{Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", 0, &TokenRefreshError{Err: err}
	}

	if resp.StatusCode != http.StatusOK {
		// Token may be revoked
		revoked := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized
		return "", "", 0, &TokenRefreshError{
			Revoked: revoked,
			Err:     fmt.Errorf("refresh failed with status %d: %s", resp.StatusCode, string(body)),
		}
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", 0, &TokenRefreshError{Err: err}
	}

	return result.AccessToken, result.RefreshToken, result.ExpiresIn, nil
}
