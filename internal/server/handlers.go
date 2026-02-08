package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/models"
)

// Session keys
const (
	sessionName          = "chatpulse-session"
	sessionKeyToken      = "token"
	sessionKeyOAuthState = "oauth_state"
)

// Twitch API endpoints and OAuth scopes
const (
	twitchAuthURL  = "https://id.twitch.tv/oauth2/authorize"
	twitchTokenURL = "https://id.twitch.tv/oauth2/token"
	twitchUsersURL = "https://api.twitch.tv/helix/users"
	twitchScopeBot = "channel:bot"
)

// Validation limits
const (
	maxTriggerLen = 500
	maxLabelLen   = 50
	minDecaySpeed = 0.1
	maxDecaySpeed = 2.0
)

// HTTP timeouts
const (
	oauthTimeout    = 10 * time.Second
	httpCallTimeout = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for OBS browser source
	},
}

// --- Template rendering helper ---

// renderTemplate renders a template to a buffer first to prevent partial HTML
// from being sent if template execution fails.
func renderTemplate(c echo.Context, tmpl *template.Template, data interface{}) error {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("Template execution failed for %s: %v", c.Request().URL.Path, err)
		return c.String(500, "Failed to render page")
	}
	return c.HTMLBlob(200, buf.Bytes())
}

// --- Auth middleware ---

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

		// Store userID in context for handlers
		c.Set("userID", userUUID)
		return next(c)
	}
}

// --- Auth handlers ---

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

	// Store state in session for CSRF verification
	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		log.Printf("Warning: failed to get session for OAuth state: %v", err)
	}
	session.Values[sessionKeyOAuthState] = state
	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		log.Printf("Failed to save OAuth state session: %v", err)
		return c.String(500, "Internal error")
	}

	// Build Twitch OAuth URL with required scope and CSRF state
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

	// Verify CSRF state parameter
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

	// Exchange code for access token with timeout
	ctx, cancel := context.WithTimeout(c.Request().Context(), oauthTimeout)
	defer cancel()

	result, err := s.exchangeCodeForToken(ctx, code)
	if err != nil {
		log.Printf("Failed to exchange code: %v", err)
		return c.String(500, "Failed to authenticate with Twitch")
	}

	// Save user to database
	tokenExpiry := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	user, err := s.db.UpsertUser(ctx, result.UserID, result.Username, result.AccessToken, result.RefreshToken, tokenExpiry)
	if err != nil {
		log.Printf("Failed to save user: %v", err)
		return c.String(500, "Failed to save user")
	}

	// Store user ID in existing session (reused from state verification above)
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
		// Create fresh session for logout
		session, err = s.sessionStore.New(c.Request(), sessionName)
		if err != nil {
			log.Printf("Error: failed to create new session during logout: %v", err)
			// Continue anyway - best effort logout
		}
	}
	session.Options.MaxAge = -1

	if err := session.Save(c.Request(), c.Response().Writer); err != nil {
		log.Printf("Failed to save logout session: %v", err)
		return c.String(500, "Failed to logout due to session error. Please try again or clear your browser cookies.")
	}

	return c.Redirect(302, "/auth/login")
}

// twitchTokenResult holds the result of a Twitch OAuth token exchange + user info fetch.
type twitchTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	UserID       string
	Username     string
}

func (s *Server) exchangeCodeForToken(ctx context.Context, code string) (*twitchTokenResult, error) {
	accessToken, refreshToken, expiresIn, err := s.exchangeCode(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	userID, username, err := s.fetchTwitchUser(ctx, accessToken)
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

func (s *Server) exchangeCode(ctx context.Context, code string) (accessToken, refreshToken string, expiresIn int, err error) {
	data := url.Values{}
	data.Set("client_id", s.config.TwitchClientID)
	data.Set("client_secret", s.config.TwitchClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", s.config.TwitchRedirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", twitchTokenURL, nil)
	if err != nil {
		return
	}
	req.URL.RawQuery = data.Encode()

	client := &http.Client{Timeout: httpCallTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("twitch returned status %d", resp.StatusCode)
		return
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}

	if err = json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return
	}

	return tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, nil
}

func (s *Server) fetchTwitchUser(ctx context.Context, accessToken string) (userID, username string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", twitchUsersURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Client-Id", s.config.TwitchClientID)

	client := &http.Client{Timeout: httpCallTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("twitch user API returned status %d", resp.StatusCode)
		return
	}

	var userResp struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}

	if err = json.NewDecoder(resp.Body).Decode(&userResp); err != nil {
		return
	}

	if len(userResp.Data) == 0 {
		err = fmt.Errorf("no user data returned")
		return
	}

	userID = userResp.Data[0].ID
	username = userResp.Data[0].DisplayName
	if username == "" {
		username = userResp.Data[0].Login
	}

	return
}

// --- Dashboard handlers ---

func (s *Server) handleDashboard(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return c.String(500, "Internal error: invalid user ID")
	}
	ctx := c.Request().Context()

	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return c.String(500, "Failed to load user")
	}

	config, err := s.db.GetConfig(ctx, userID)
	if err != nil {
		return c.String(500, "Failed to load config")
	}

	overlayURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), user.OverlayUUID)

	data := map[string]interface{}{
		"Username":       user.TwitchUsername,
		"OverlayURL":     overlayURL,
		"OverlayUUID":    user.OverlayUUID.String(),
		"ForTrigger":     config.ForTrigger,
		"AgainstTrigger": config.AgainstTrigger,
		"LeftLabel":      config.LeftLabel,
		"RightLabel":     config.RightLabel,
		"DecaySpeed":     config.DecaySpeed,
	}

	return renderTemplate(c, s.dashboardTemplate, data)
}

// validateConfig validates config form inputs
func validateConfig(forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	// Trigger validation
	if strings.TrimSpace(forTrigger) == "" {
		return fmt.Errorf("for trigger cannot be empty")
	}
	if strings.TrimSpace(againstTrigger) == "" {
		return fmt.Errorf("against trigger cannot be empty")
	}

	// Triggers must be different (case-insensitive, since vote matching is case-insensitive)
	if strings.EqualFold(strings.TrimSpace(forTrigger), strings.TrimSpace(againstTrigger)) {
		return fmt.Errorf("for trigger and against trigger must be different")
	}

	// Length limits (prevent DB bloat)
	if len(forTrigger) > maxTriggerLen {
		return fmt.Errorf("for trigger exceeds %d characters", maxTriggerLen)
	}
	if len(againstTrigger) > maxTriggerLen {
		return fmt.Errorf("against trigger exceeds %d characters", maxTriggerLen)
	}
	if len(leftLabel) > maxLabelLen {
		return fmt.Errorf("left label exceeds %d characters", maxLabelLen)
	}
	if len(rightLabel) > maxLabelLen {
		return fmt.Errorf("right label exceeds %d characters", maxLabelLen)
	}

	// Decay range (must match slider in template: 0.1-2.0)
	if decaySpeed < minDecaySpeed || decaySpeed > maxDecaySpeed {
		return fmt.Errorf("decay speed must be between %.1f and %.1f", minDecaySpeed, maxDecaySpeed)
	}

	return nil
}

func (s *Server) handleSaveConfig(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return c.String(500, "Internal error: invalid user ID")
	}
	ctx := c.Request().Context()

	// Parse form
	forTrigger := c.FormValue("for_trigger")
	againstTrigger := c.FormValue("against_trigger")
	leftLabel := c.FormValue("left_label")
	rightLabel := c.FormValue("right_label")

	var decaySpeed float64
	if _, err := fmt.Sscanf(c.FormValue("decay_speed"), "%f", &decaySpeed); err != nil {
		return c.String(400, "Invalid decay speed")
	}

	// Validate inputs
	if err := validateConfig(forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed); err != nil {
		return c.String(400, fmt.Sprintf("Validation error: %v", err))
	}

	// Update database
	if err := s.db.UpdateConfig(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed); err != nil {
		log.Printf("Failed to update config: %v", err)
		return c.String(500, "Failed to save config")
	}

	// Update in-memory config for active sessions
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		log.Printf("Failed to get user for live config update: %v", err)
	} else {
		snapshot := models.ConfigSnapshot{
			ForTrigger:     forTrigger,
			AgainstTrigger: againstTrigger,
			LeftLabel:      leftLabel,
			RightLabel:     rightLabel,
			DecaySpeed:     decaySpeed,
		}
		s.sentiment.UpdateSessionConfig(user.OverlayUUID, snapshot)
	}

	return c.Redirect(302, "/dashboard")
}

// --- API handlers ---

func (s *Server) handleResetSentiment(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return c.String(500, "Internal error: invalid user ID")
	}
	overlayUUIDStr := c.Param("uuid")

	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.JSON(400, map[string]string{"error": "Invalid UUID"})
	}

	ctx := c.Request().Context()

	// Verify the UUID belongs to the authenticated user
	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return c.JSON(500, map[string]string{"error": "Failed to load user"})
	}

	if user.OverlayUUID != overlayUUID {
		return c.JSON(403, map[string]string{"error": "Forbidden"})
	}

	s.sentiment.ResetSession(overlayUUID)
	return c.JSON(200, map[string]string{"status": "ok"})
}

func (s *Server) handleRotateOverlayUUID(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return c.String(500, "Internal error: invalid user ID")
	}
	ctx := c.Request().Context()

	newUUID, err := s.db.RotateOverlayUUID(ctx, userID)
	if err != nil {
		log.Printf("Failed to rotate overlay UUID: %v", err)
		return c.JSON(500, map[string]string{"error": "Failed to rotate URL"})
	}

	newURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), newUUID)
	return c.JSON(200, map[string]interface{}{
		"status":   "ok",
		"new_uuid": newUUID.String(),
		"new_url":  newURL,
	})
}

// --- Overlay handler ---

func (s *Server) handleOverlay(c echo.Context) error {
	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.String(400, "Invalid UUID")
	}

	ctx := c.Request().Context()

	// Look up user by overlay UUID to get config
	user, err := s.db.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.String(404, "Overlay not found")
	}
	if err != nil {
		log.Printf("Failed to get user by overlay UUID: %v", err)
		return c.String(500, "Failed to load overlay")
	}

	// Get config for labels
	config, err := s.db.GetConfig(ctx, user.ID)
	if err != nil {
		log.Printf("Failed to load config: %v", err)
		return c.String(500, "Failed to load config")
	}

	data := map[string]interface{}{
		"LeftLabel":   config.LeftLabel,
		"RightLabel":  config.RightLabel,
		"WSHost":      c.Request().Host,
		"SessionUUID": overlayUUIDStr,
	}

	return renderTemplate(c, s.overlayTemplate, data)
}

// --- WebSocket handler ---

func (s *Server) handleWebSocket(c echo.Context) error {
	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.String(400, "Invalid UUID")
	}

	ctx := c.Request().Context()

	// Look up user by overlay UUID
	user, err := s.db.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.String(404, "Session not found")
	}
	if err != nil {
		log.Printf("Failed to get user by overlay UUID: %v", err)
		return c.String(500, "Internal error")
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		// Upgrade writes its own HTTP error response, so return the error
		// for logging only — Echo won't double-write after Upgrade.
		return fmt.Errorf("failed to upgrade WebSocket: %w", err)
	}

	// Register with hub (onFirstConnect callback handles activation and subscription)
	if err := s.hub.Register(user.OverlayUUID, conn); err != nil {
		log.Printf("Failed to register with hub: %v", err)
		// Connection already closed by hub, just return
		return nil
	}

	// Read pump (blocks until disconnect)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	// Unregister and mark disconnected
	s.hub.Unregister(user.OverlayUUID, conn)
	s.sentiment.MarkDisconnected(user.OverlayUUID)

	return nil //nolint:nilerr // ReadMessage err is block-scoped; outer err is nil
}

// --- Helpers ---

func (s *Server) getBaseURL(c echo.Context) string {
	scheme := "http"
	if c.Request().TLS != nil {
		scheme = "https"
	}
	if fwdProto := c.Request().Header.Get("X-Forwarded-Proto"); fwdProto != "" {
		scheme = fwdProto
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request().Host)
}
