package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
	"github.com/pscheid92/chatpulse/internal/metrics"
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
		return apperrors.ValidationError("invalid UUID format").WithField("uuid", overlayUUIDStr)
	}

	ctx := c.Request().Context()

	user, err := s.app.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return apperrors.NotFoundError("overlay not found").WithField("overlay_uuid", overlayUUID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to load overlay", err).
			WithField("overlay_uuid", overlayUUID.String())
	}

	config, err := s.app.GetConfig(ctx, user.ID)
	if err != nil {
		if errors.Is(err, domain.ErrConfigNotFound) {
			return apperrors.NotFoundError("config not found").
				WithField("user_id", user.ID.String()).
				WithField("overlay_uuid", overlayUUID.String())
		}
		return apperrors.InternalError("failed to load config", err).
			WithField("user_id", user.ID.String()).
			WithField("overlay_uuid", overlayUUID.String())
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
	// Track connection start time for duration metric
	connectionStart := time.Now()

	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		metrics.WebSocketConnectionsTotal.WithLabelValues("error").Inc()
		return apperrors.ValidationError("invalid UUID format").WithField("uuid", overlayUUIDStr)
	}

	// Extract client IP
	clientIP := c.RealIP()

	// Check connection limits (Layer 3: rate → Layer 1: global → Layer 2: per-IP)
	allowed, reason := s.connLimits.Acquire(clientIP)
	if !allowed {
		switch reason {
		case LimitReasonRate:
			metrics.WebSocketConnectionsRejected.WithLabelValues("rate_limit").Inc()
			slog.Warn("WebSocket connection rate limited", "ip", clientIP, "reason", reason)
			return apperrors.ValidationError("too many connection attempts").
				WithField("client_ip", clientIP).
				WithField("reason", "rate_limit")
		case LimitReasonGlobal:
			metrics.WebSocketConnectionsRejected.WithLabelValues("global_limit").Inc()
			slog.Warn("WebSocket connection rejected: global limit", "ip", clientIP, "capacity_pct", s.connLimits.Global().CapacityPct())
			return apperrors.InternalError("server at capacity", nil).
				WithField("client_ip", clientIP).
				WithField("capacity_pct", s.connLimits.Global().CapacityPct())
		case LimitReasonPerIP:
			metrics.WebSocketConnectionsRejected.WithLabelValues("ip_limit").Inc()
			slog.Warn("WebSocket connection rejected: per-IP limit", "ip", clientIP, "connections", s.connLimits.PerIP().Count(clientIP))
			return apperrors.ValidationError("too many connections from your IP address").
				WithField("client_ip", clientIP).
				WithField("connections", s.connLimits.PerIP().Count(clientIP))
		default:
			metrics.WebSocketConnectionsRejected.WithLabelValues("unknown").Inc()
			return apperrors.InternalError("connection limit exceeded", nil).
				WithField("client_ip", clientIP)
		}
	}
	// Ensure we release the connection slot when the handler exits
	defer s.connLimits.Release(clientIP)

	// Update capacity metrics
	metrics.WebSocketConnectionCapacity.Set(s.connLimits.Global().CapacityPct())
	metrics.WebSocketUniqueIPs.Set(float64(s.connLimits.PerIP().UniqueIPs()))

	ctx := c.Request().Context()

	user, err := s.app.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, domain.ErrUserNotFound) {
		return apperrors.NotFoundError("session not found").WithField("overlay_uuid", overlayUUID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to get user by overlay UUID", err).
			WithField("overlay_uuid", overlayUUID.String())
	}

	// Ensure session is active in Redis (activate or resume)
	if err := s.app.EnsureSessionActive(ctx, user.OverlayUUID); err != nil {
		return apperrors.InternalError("failed to activate session", err).
			WithField("overlay_uuid", user.OverlayUUID.String()).
			WithField("user_id", user.ID.String())
	}

	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return apperrors.InternalError("failed to upgrade WebSocket", err)
	}

	if err := s.broadcaster.Register(user.OverlayUUID, conn); err != nil {
		conn.Close()
		return apperrors.InternalError("failed to register client with broadcaster", err).
			WithField("overlay_uuid", user.OverlayUUID.String())
	}

	// Track successful connection
	metrics.WebSocketConnectionsTotal.WithLabelValues("success").Inc()
	metrics.WebSocketConnectionsCurrent.Inc()

	// Record metrics when the handler exits
	defer func() {
		duration := time.Since(connectionStart).Seconds()
		metrics.WebSocketConnectionDuration.Observe(duration)
		metrics.WebSocketConnectionsCurrent.Dec()
	}()

	// Read pump — blocks until connection closes
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	s.broadcaster.Unregister(user.OverlayUUID, conn)

	return nil //nolint:nilerr // ReadMessage err is block-scoped; outer err is nil
}
