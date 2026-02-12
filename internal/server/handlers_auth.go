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
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
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
		return apperrors.InternalError("failed to generate OAuth state", err)
	}

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		slog.Error("Warning: failed to get session for OAuth state", "error", err)
	}
	session.Values[sessionKeyOAuthState] = state
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		return apperrors.InternalError("failed to save OAuth state session", err)
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
		return apperrors.ValidationError("missing code parameter")
	}

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		return apperrors.ValidationError("invalid session")
	}
	expectedState, ok := session.Values[sessionKeyOAuthState].(string)
	if !ok || expectedState == "" {
		return apperrors.ValidationError("missing OAuth state")
	}
	if c.QueryParam("state") != expectedState {
		return apperrors.ValidationError("invalid OAuth state")
	}
	delete(session.Values, sessionKeyOAuthState)

	ctx, cancel := context.WithTimeout(c.Request().Context(), oauthTimeout)
	defer cancel()

	result, err := s.oauthClient.ExchangeCodeForToken(ctx, code)
	if err != nil {
		return apperrors.ExternalError("failed to authenticate with Twitch", err)
	}

	tokenExpiry := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	user, err := s.app.UpsertUser(ctx, result.UserID, result.Username, result.AccessToken, result.RefreshToken, tokenExpiry)
	if err != nil {
		return apperrors.InternalError("failed to save user", err).
			WithField("twitch_user_id", result.UserID)
	}

	// Regenerate session ID after successful authentication to prevent session fixation attacks.
	// This creates a new session with a fresh ID, invalidating any pre-auth session ID.
	// Defense-in-depth: Even though we use HTTPS, HttpOnly, and SameSite=Lax, session
	// regeneration ensures that an attacker who fixated a session ID before login cannot
	// hijack the authenticated session afterward.
	session.Options.MaxAge = -1 // Mark old session for deletion
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		return apperrors.InternalError("failed to invalidate old session", err)
	}

	// Create new session with fresh ID
	session, err = s.sessionStore.New(c.Request(), sessionName)
	if err != nil {
		return apperrors.InternalError("failed to create new session", err)
	}

	session.Values[sessionKeyToken] = user.ID.String()
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		return apperrors.InternalError("failed to save session", err)
	}

	return c.Redirect(302, "/dashboard")
}

func (s *Server) handleLogout(c echo.Context) error {
	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		slog.Error("Warning: failed to get session during logout", "error", err)
		session, err = s.sessionStore.New(c.Request(), sessionName)
		if err != nil {
			return apperrors.InternalError("failed to create new session during logout", err)
		}
	}
	session.Options.MaxAge = -1

	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		return apperrors.InternalError("failed to save logout session", err)
	}

	return c.Redirect(302, "/auth/login")
}
