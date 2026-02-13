package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	twitchTokenURL  = "https://id.twitch.tv/oauth2/token"
	twitchUsersURL  = "https://api.twitch.tv/helix/users"
	httpCallTimeout = 10 * time.Second
)

// twitchOAuthClient handles the Twitch OAuth token exchange and user info fetch.
type twitchOAuthClient interface {
	ExchangeCodeForToken(ctx context.Context, code string) (*twitchTokenResult, error)
}

// twitchTokenResult holds the result of a Twitch OAuth token exchange + user info fetch.
type twitchTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	UserID       string
	Username     string
}

// twitchOAuthHTTPClient is the production implementation using Twitch HTTP APIs.
type twitchOAuthHTTPClient struct {
	clientID     string
	clientSecret string
	redirectURI  string
}

func newTwitchOAuthClient(clientID, clientSecret, redirectURI string) *twitchOAuthHTTPClient {
	return &twitchOAuthHTTPClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
	}
}

func (c *twitchOAuthHTTPClient) ExchangeCodeForToken(ctx context.Context, code string) (*twitchTokenResult, error) {
	accessToken, refreshToken, expiresIn, err := c.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	userID, username, err := c.fetchTwitchUser(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("user info fetch failed: %w", err)
	}

	return &twitchTokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    expiresIn,
		UserID:       userID,
		Username:     username,
	}, nil
}

func (c *twitchOAuthHTTPClient) exchangeCode(ctx context.Context, code string) (string, string, int, error) {
	data := url.Values{}
	data.Set("client_id", c.clientID)
	data.Set("client_secret", c.clientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", c.redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", twitchTokenURL, nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to create token request: %w", err)
	}
	req.URL.RawQuery = data.Encode()

	client := &http.Client{Timeout: httpCallTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to execute token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("twitch returned status %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", 0, fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, nil
}

func (c *twitchOAuthHTTPClient) fetchTwitchUser(ctx context.Context, accessToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", twitchUsersURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Client-Id", c.clientID)

	client := &http.Client{Timeout: httpCallTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to execute user request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("twitch user API returned status %d", resp.StatusCode)
	}

	var userResp struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&userResp); err != nil {
		return "", "", fmt.Errorf("failed to decode user response: %w", err)
	}

	if len(userResp.Data) == 0 {
		return "", "", fmt.Errorf("no user data returned")
	}

	username := userResp.Data[0].DisplayName
	if username == "" {
		username = userResp.Data[0].Login
	}

	return userResp.Data[0].ID, username, nil
}
