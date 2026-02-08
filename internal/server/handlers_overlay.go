package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for OBS browser source
	},
}

func (s *Server) handleOverlay(c echo.Context) error {
	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.String(400, "Invalid UUID")
	}

	ctx := c.Request().Context()

	user, err := s.db.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.String(404, "Overlay not found")
	}
	if err != nil {
		log.Printf("Failed to get user by overlay UUID: %v", err)
		return c.String(500, "Failed to load overlay")
	}

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

func (s *Server) handleWebSocket(c echo.Context) error {
	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return c.String(400, "Invalid UUID")
	}

	ctx := c.Request().Context()

	user, err := s.db.GetUserByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.String(404, "Session not found")
	}
	if err != nil {
		log.Printf("Failed to get user by overlay UUID: %v", err)
		return c.String(500, "Internal error")
	}

	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return fmt.Errorf("failed to upgrade WebSocket: %w", err)
	}

	if err := s.hub.Register(user.OverlayUUID, conn); err != nil {
		log.Printf("Failed to register with hub: %v", err)
		return nil
	}

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	s.hub.Unregister(user.OverlayUUID, conn)
	s.sentiment.MarkDisconnected(user.OverlayUUID)

	return nil //nolint:nilerr // ReadMessage err is block-scoped; outer err is nil
}
