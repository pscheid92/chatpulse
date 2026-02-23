package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/labstack/echo/v4"
	apperrors "github.com/pscheid92/chatpulse/internal/platform/errors"
	"github.com/pscheid92/uuid"
)

const (
	twitchAuthURL  = "https://id.twitch.tv/oauth2/authorize"
	twitchScopeBot = "channel:bot"
	oauthTimeout   = 10 * time.Second
)

func (s *Server) registerAuthRoutes(csrfMiddleware, rateLimiter echo.MiddlewareFunc) {
	s.echo.GET("/auth/login", s.handleLoginPage, rateLimiter)
	s.echo.GET("/auth/callback", s.handleOAuthCallback, rateLimiter)
	s.echo.POST("/auth/logout", s.handleLogout, rateLimiter, s.requireAuth, csrfMiddleware)
}

func (s *Server) handleLanding(c echo.Context) error {
	if s.isAuthenticated(c) {
		if err := c.Redirect(http.StatusFound, "/dashboard"); err != nil {
			return fmt.Errorf("failed to redirect: %w", err)
		}
		return nil
	}
	return s.renderTemplate(c, "landing.html", nil)
}

func (s *Server) requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		session, err := s.sessionStore.Get(c.Request(), sessionName)
		if err != nil {
			return c.Redirect(http.StatusFound, "/auth/login")
		}

		userID, ok := session.Values[sessionKeyToken]
		if !ok {
			return c.Redirect(http.StatusFound, "/auth/login")
		}

		userIDStr, ok := userID.(string)
		if !ok {
			return c.Redirect(http.StatusFound, "/auth/login")
		}

		userUUID, err := uuid.Parse(userIDStr)
		if err != nil {
			return c.Redirect(http.StatusFound, "/auth/login")
		}

		// Verify the streamer still exists in the DB (handles wiped DB, deleted accounts).
		if _, err := s.app.GetStreamerByID(c.Request().Context(), userUUID); err != nil {
			slog.Warn("Session references unknown streamer, invalidating", "streamer_id", userUUID)
			session.Options.MaxAge = -1
			_ = session.Save(c.Request(), c.Response().Writer)
			return c.Redirect(http.StatusFound, "/auth/login")
		}

		c.Set("userID", userUUID)
		return next(c)
	}
}

// isAuthenticated checks whether the request has a valid session for an existing streamer.
func (s *Server) isAuthenticated(c echo.Context) bool {
	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		return false
	}
	userIDStr, ok := session.Values[sessionKeyToken].(string)
	if !ok {
		return false
	}
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		return false
	}
	_, err = s.app.GetStreamerByID(c.Request().Context(), userUUID)
	return err == nil
}

func generateOAuthState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate OAuth state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Server) handleLoginPage(c echo.Context) error {
	// Redirect already-authenticated users to dashboard (avoids Twitch's redirect page).
	if s.isAuthenticated(c) {
		if err := c.Redirect(http.StatusFound, "/dashboard"); err != nil {
			return fmt.Errorf("failed to redirect: %w", err)
		}
		return nil
	}

	state, err := generateOAuthState()
	if err != nil {
		return apperrors.InternalError("failed to generate OAuth state", err)
	}

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		slog.Error("Failed to get session for OAuth state", "error", err)
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

	data := map[string]any{"TwitchAuthURL": authURL}
	return s.renderTemplate(c, "login.html", data)
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

	streamer, err := s.app.UpsertStreamer(ctx, result.UserID, result.Username)
	if err != nil {
		return apperrors.InternalError("failed to save streamer", err).WithField("twitch_user_id", result.UserID)
	}

	if subErr := s.twitchService.Subscribe(ctx, streamer.ID, result.UserID); subErr != nil {
		slog.Error("Failed to create EventSub subscription during OAuth callback", "streamer_id", streamer.ID, "twitch_user_id", result.UserID, "error", subErr)
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

	// Create a new session with fresh ID
	session, err = s.sessionStore.New(c.Request(), sessionName)
	if err != nil {
		return apperrors.InternalError("failed to create new session", err)
	}

	session.Values[sessionKeyToken] = streamer.ID.String()
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		return apperrors.InternalError("failed to save session", err)
	}

	slog.InfoContext(ctx, "Streamer logged in", "streamer_id", streamer.ID, "broadcaster_id", result.UserID, "twitch_username", result.Username)

	if err := c.Redirect(http.StatusFound, "/dashboard"); err != nil {
		return fmt.Errorf("failed to redirect: %w", err)
	}
	return nil
}

func (s *Server) handleLogout(c echo.Context) error {
	ctx := c.Request().Context()
	streamerID, _ := c.Get("userID").(uuid.UUID)

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		slog.Error("Failed to get session during logout", "error", err)
		session, err = s.sessionStore.New(c.Request(), sessionName)
		if err != nil {
			return apperrors.InternalError("failed to create new session during logout", err)
		}
	}
	session.Options.MaxAge = -1

	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		return apperrors.InternalError("failed to save logout session", err)
	}

	slog.InfoContext(ctx, "Streamer logged out", "streamer_id", streamerID)

	if err := c.Redirect(http.StatusFound, "/auth/login"); err != nil {
		return fmt.Errorf("failed to redirect: %w", err)
	}
	return nil
}
