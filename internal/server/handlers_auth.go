package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
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
		log.Printf("Failed to generate OAuth state: %v", err)
		return c.String(500, "Internal error")
	}

	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		log.Printf("Warning: failed to get session for OAuth state: %v", err)
	}
	session.Values[sessionKeyOAuthState] = state
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		log.Printf("Failed to save OAuth state session: %v", err)
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

	data := map[string]interface{}{
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
		log.Printf("Failed to exchange code: %v", err)
		return c.String(500, "Failed to authenticate with Twitch")
	}

	tokenExpiry := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	user, err := s.db.UpsertUser(ctx, result.UserID, result.Username, result.AccessToken, result.RefreshToken, tokenExpiry)
	if err != nil {
		log.Printf("Failed to save user: %v", err)
		return c.String(500, "Failed to save user")
	}

	session.Values[sessionKeyToken] = user.ID.String()
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		log.Printf("Failed to save session: %v", err)
		return c.String(500, "Failed to save session")
	}

	return c.Redirect(302, "/dashboard")
}

func (s *Server) handleLogout(c echo.Context) error {
	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		log.Printf("Warning: failed to get session during logout: %v", err)
		session, err = s.sessionStore.New(c.Request(), sessionName)
		if err != nil {
			log.Printf("Error: failed to create new session during logout: %v", err)
		}
	}
	session.Options.MaxAge = -1

	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		log.Printf("Failed to save logout session: %v", err)
		return c.String(500, "Failed to logout due to session error. Please try again or clear your browser cookies.")
	}

	return c.Redirect(302, "/auth/login")
}
