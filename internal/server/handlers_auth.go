package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

const (
	twitchAuthURL  = "https://id.twitch.tv/oauth2/authorize"
	twitchScopeBot = "channel:bot"
	oauthTimeout   = 10 * time.Second
)

func (s *Server) requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		session, err := s.sessionStore.Get(c.Request(), sessionName)
		if err != nil {
			return c.Redirect(302, "/auth/login")
		}

		userID, ok := session.Values[sessionKeyToken]
		if !ok {
			return c.Redirect(302, "/auth/login")
		}

		userIDStr, ok := userID.(string)
		if !ok {
			return c.Redirect(302, "/auth/login")
		}

		userUUID, err := uuid.Parse(userIDStr)
		if err != nil {
			return c.Redirect(302, "/auth/login")
		}

		c.Set("userID", userUUID)
		return next(c)
	}
}

func generateOAuthState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) handleLoginPage(c echo.Context) error {
	state, err := generateOAuthState()
	if err != nil {
		slog.Error("Failed to generate OAuth state", "error", err)
		return c.String(500, "Internal error")
	}

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		slog.Error("Warning: failed to get session for OAuth state", "error", err)
	}
	session.Values[sessionKeyOAuthState] = state
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		slog.Error("Failed to save OAuth state session", "error", err)
		return c.String(500, "Internal error")
	}

	authURL := fmt.Sprintf(
		"%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&state=%s",
		twitchAuthURL,
		url.QueryEscape(s.config.TwitchClientID),
		url.QueryEscape(s.config.TwitchRedirectURI),
		url.QueryEscape(twitchScopeBot),
		url.QueryEscape(state),
	)

	data := map[string]any{
		"TwitchAuthURL": authURL,
	}

	return renderTemplate(c, s.loginTemplate, data)
}

func (s *Server) handleOAuthCallback(c echo.Context) error {
	code := c.QueryParam("code")
	if code == "" {
		return c.String(400, "Missing code parameter")
	}

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		return c.String(400, "Invalid session")
	}
	expectedState, ok := session.Values[sessionKeyOAuthState].(string)
	if !ok || expectedState == "" {
		return c.String(400, "Missing OAuth state")
	}
	if c.QueryParam("state") != expectedState {
		return c.String(400, "Invalid OAuth state")
	}
	delete(session.Values, sessionKeyOAuthState)

	ctx, cancel := context.WithTimeout(c.Request().Context(), oauthTimeout)
	defer cancel()

	result, err := s.oauthClient.ExchangeCodeForToken(ctx, code)
	if err != nil {
		slog.Error("Failed to exchange OAuth code", "error", err)
		return c.String(500, "Failed to authenticate with Twitch")
	}

	tokenExpiry := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	user, err := s.app.UpsertUser(ctx, result.UserID, result.Username, result.AccessToken, result.RefreshToken, tokenExpiry)
	if err != nil {
		slog.Error("Failed to save user", "error", err)
		return c.String(500, "Failed to save user")
	}

	// Regenerate session ID after successful authentication to prevent session fixation attacks.
	// This creates a new session with a fresh ID, invalidating any pre-auth session ID.
	// Defense-in-depth: Even though we use HTTPS, HttpOnly, and SameSite=Lax, session
	// regeneration ensures that an attacker who fixated a session ID before login cannot
	// hijack the authenticated session afterward.
	session.Options.MaxAge = -1 // Mark old session for deletion
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		slog.Error("Failed to invalidate old session", "error", err)
		return c.String(500, "Failed to save session")
	}

	// Create new session with fresh ID
	session, err = s.sessionStore.New(c.Request(), sessionName)
	if err != nil {
		slog.Error("Failed to create new session", "error", err)
		return c.String(500, "Failed to save session")
	}

	session.Values[sessionKeyToken] = user.ID.String()
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		slog.Error("Failed to save session", "error", err)
		return c.String(500, "Failed to save session")
	}

	return c.Redirect(302, "/dashboard")
}

func (s *Server) handleLogout(c echo.Context) error {
	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		slog.Error("Warning: failed to get session during logout", "error", err)
		session, err = s.sessionStore.New(c.Request(), sessionName)
		if err != nil {
			slog.Error("Error: failed to create new session during logout", "error", err)
		}
	}
	session.Options.MaxAge = -1

	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		slog.Error("Failed to save logout session", "error", err)
		return c.String(500, "Failed to logout due to session error. Please try again or clear your browser cookies.")
	}

	return c.Redirect(302, "/auth/login")
}
