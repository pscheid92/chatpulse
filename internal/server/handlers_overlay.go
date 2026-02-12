package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// upgrader configures WebSocket upgrade behavior for overlay connections.
//
// Security Note - CORS Policy:
// CheckOrigin accepts all origins (returns true) to support OBS browser sources,
// which connect from obs:// protocol origins that cannot be allowlisted.
//
// This is intentionally permissive because:
// 1. Overlay data (sentiment values) is public by design - streamers share overlay URLs
// 2. WebSocket connections are read-only (client receives broadcasts, cannot send commands)
// 3. Access control is via unguessable overlay UUIDs (effectively bearer tokens)
// 4. No sensitive user data is transmitted (no PII, tokens, or credentials)
//
// Security properties:
// - UUIDs are cryptographically random (128-bit), not user-controlled
// - Streamers can rotate UUIDs to invalidate old URLs (POST /api/rotate-overlay-uuid)
// - No authentication cookies/headers are sent over WebSocket connections
// - Broadcast-only pattern prevents command injection or tampering
//
// Threat model considerations:
// - Overlay URL leakage: Mitigated by UUID rotation capability
// - Third-party embedding: Acceptable - overlay data is public by design
// - Cross-site WebSocket hijacking (CSWSH): Not applicable - no state-changing operations
// - Analytics/tracking via embedding: Acceptable trade-off for OBS compatibility
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // See security note above
	},
}

func (s *Server) handleOverlay(c echo.Context) error {
	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.String(400, "Invalid UUID")
	}

	ctx := c.Request().Context()

	user, err := s.app.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return c.String(404, "Overlay not found")
	}
	if err != nil {
		slog.Error("Failed to get user by overlay UUID", "error", err)
		return c.String(500, "Failed to load overlay")
	}

	config, err := s.app.GetConfig(ctx, user.ID)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		return c.String(500, "Failed to load config")
	}

	data := map[string]any{
		"LeftLabel":   config.LeftLabel,
		"RightLabel":  config.RightLabel,
		"WSHost":      c.Request().Host,
		"SessionUUID": overlayUUIDStr,
	}

	return renderTemplate(c, s.overlayTemplate, data)
}

func (s *Server) handleWebSocket(c echo.Context) error {
	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.String(400, "Invalid UUID")
	}

	ctx := c.Request().Context()

	user, err := s.app.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return c.String(404, "Session not found")
	}
	if err != nil {
		slog.Error("Failed to get user by overlay UUID", "error", err)
		return c.String(500, "Internal error")
	}

	// Ensure session is active in Redis (activate or resume)
	if err := s.app.EnsureSessionActive(ctx, user.OverlayUUID); err != nil {
		slog.Error("Failed to ensure session active", "error", err)
		return c.String(500, "Failed to activate session")
	}

	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return fmt.Errorf("failed to upgrade WebSocket: %w", err)
	}

	if err := s.broadcaster.Register(user.OverlayUUID, conn); err != nil {
		slog.Error("Failed to register with broadcaster", "error", err)
		conn.Close()
		return fmt.Errorf("failed to register client with broadcaster: %w", err)
	}

	// Read pump — blocks until connection closes
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	s.broadcaster.Unregister(user.OverlayUUID, conn)

	return nil //nolint:nilerr // ReadMessage err is block-scoped; outer err is nil
}
