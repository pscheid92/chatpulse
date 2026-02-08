package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/pscheid92/twitch-tow/internal/models"
)

// Session keys
const (
	sessionName     = "twitch-tow-session"
	sessionKeyToken = "token"
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

func (s *Server) handleLoginPage(c echo.Context) error {
	tmpl, err := template.ParseFiles("web/templates/login.html")
	if err != nil {
		return c.String(500, "Failed to load template")
	}

	// Build Twitch OAuth URL with required scope
	twitchAuthURL := fmt.Sprintf(
		"https://id.twitch.tv/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=user:read:chat",
		url.QueryEscape(s.config.TwitchClientID),
		url.QueryEscape(s.config.TwitchRedirectURI),
	)

	data := map[string]interface{}{
		"TwitchAuthURL": twitchAuthURL,
	}

	return renderTemplate(c, tmpl, data)
}

func (s *Server) handleOAuthCallback(c echo.Context) error {
	code := c.QueryParam("code")
	if code == "" {
		return c.String(400, "Missing code parameter")
	}

	// Exchange code for access token with timeout
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()

	accessToken, refreshToken, expiresIn, twitchUserID, twitchUsername, err := s.exchangeCodeForToken(ctx, code)
	if err != nil {
		log.Printf("Failed to exchange code: %v", err)
		return c.String(500, "Failed to authenticate with Twitch")
	}

	// Save user to database
	tokenExpiry := time.Now().Add(time.Duration(expiresIn) * time.Second)
	user, err := s.db.UpsertUser(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
	if err != nil {
		log.Printf("Failed to save user: %v", err)
		return c.String(500, "Failed to save user")
	}

	// Create session
	session, err := s.sessionStore.Get(c.Request(), sessionName)
	if err != nil {
		log.Printf("Warning: failed to get session during OAuth: %v", err)
		// Continue with new session
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

func (s *Server) exchangeCodeForToken(ctx context.Context, code string) (accessToken, refreshToken string, expiresIn int, twitchUserID, twitchUsername string, err error) {
	// Exchange code for token
	data := url.Values{}
	data.Set("client_id", s.config.TwitchClientID)
	data.Set("client_secret", s.config.TwitchClientSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", s.config.TwitchRedirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://id.twitch.tv/oauth2/token", nil)
	if err != nil {
		return
	}
	req.URL.RawQuery = data.Encode()

	client := &http.Client{Timeout: 10 * time.Second}
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

	accessToken = tokenResp.AccessToken
	refreshToken = tokenResp.RefreshToken
	expiresIn = tokenResp.ExpiresIn

	// Get user info
	req2, err := http.NewRequestWithContext(ctx, "GET", "https://api.twitch.tv/helix/users", nil)
	if err != nil {
		return
	}
	req2.Header.Set("Authorization", "Bearer "+accessToken)
	req2.Header.Set("Client-Id", s.config.TwitchClientID)

	resp2, err := client.Do(req2)
	if err != nil {
		return
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		err = fmt.Errorf("twitch user API returned status %d", resp2.StatusCode)
		return
	}

	var userResp struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}

	if err = json.NewDecoder(resp2.Body).Decode(&userResp); err != nil {
		return
	}

	if len(userResp.Data) == 0 {
		err = fmt.Errorf("no user data returned")
		return
	}

	twitchUserID = userResp.Data[0].ID
	twitchUsername = userResp.Data[0].DisplayName
	if twitchUsername == "" {
		twitchUsername = userResp.Data[0].Login
	}

	return
}

// --- Dashboard handlers ---

func (s *Server) handleDashboard(c echo.Context) error {
	userID := c.Get("userID").(uuid.UUID)
	ctx := c.Request().Context()

	user, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return c.String(500, "Failed to load user")
	}

	config, err := s.db.GetConfig(ctx, userID)
	if err != nil {
		return c.String(500, "Failed to load config")
	}

	tmpl, err := template.ParseFiles("web/templates/dashboard.html")
	if err != nil {
		return c.String(500, "Failed to load template")
	}

	overlayURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), user.OverlayUUID)

	data := map[string]interface{}{
		"Username":      user.TwitchUsername,
		"OverlayURL":    overlayURL,
		"OverlayUUID":   user.OverlayUUID.String(),
		"ForTrigger":    config.ForTrigger,
		"AgainstTrigger": config.AgainstTrigger,
		"LeftLabel":     config.LeftLabel,
		"RightLabel":    config.RightLabel,
		"DecaySpeed":    config.DecaySpeed,
	}

	return renderTemplate(c, tmpl, data)
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
	if len(forTrigger) > 500 {
		return fmt.Errorf("for trigger exceeds 500 characters")
	}
	if len(againstTrigger) > 500 {
		return fmt.Errorf("against trigger exceeds 500 characters")
	}
	if len(leftLabel) > 50 {
		return fmt.Errorf("left label exceeds 50 characters")
	}
	if len(rightLabel) > 50 {
		return fmt.Errorf("right label exceeds 50 characters")
	}

	// Decay range (must match slider in template: 0.1-2.0)
	if decaySpeed < 0.1 || decaySpeed > 2.0 {
		return fmt.Errorf("decay speed must be between 0.1 and 2.0")
	}

	return nil
}

func (s *Server) handleSaveConfig(c echo.Context) error {
	userID := c.Get("userID").(uuid.UUID)
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
	if err == nil {
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
	userID := c.Get("userID").(uuid.UUID)
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
	userID := c.Get("userID").(uuid.UUID)
	ctx := c.Request().Context()

	newUUID, err := s.db.RotateOverlayUUID(ctx, userID)
	if err != nil {
		log.Printf("Failed to rotate overlay UUID: %v", err)
		return c.JSON(500, map[string]string{"error": "Failed to rotate URL"})
	}

	newURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), newUUID)
	return c.JSON(200, map[string]interface{}{
		"status":     "ok",
		"new_uuid":   newUUID.String(),
		"new_url":    newURL,
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
	if err == sql.ErrNoRows {
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

	tmpl, err := template.ParseFiles("web/templates/overlay.html")
	if err != nil {
		return c.String(500, "Failed to load template")
	}

	data := map[string]interface{}{
		"LeftLabel":   config.LeftLabel,
		"RightLabel":  config.RightLabel,
		"WSHost":      c.Request().Host,
		"SessionUUID": overlayUUIDStr,
	}

	return renderTemplate(c, tmpl, data)
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
	if err == sql.ErrNoRows {
		return c.String(404, "Session not found")
	}
	if err != nil {
		log.Printf("Failed to get user by overlay UUID: %v", err)
		return c.String(500, "Internal error")
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket: %v", err)
		return nil
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

	return nil
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
