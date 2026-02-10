package server

import (
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

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

	user, err := s.app.GetUserByID(ctx, userID)
	if err != nil {
		return c.JSON(500, map[string]string{"error": "Failed to load user"})
	}

	if user.OverlayUUID != overlayUUID {
		return c.JSON(403, map[string]string{"error": "Forbidden"})
	}

	if err := s.app.ResetSentiment(ctx, overlayUUID); err != nil {
		log.Printf("Failed to reset sentiment: %v", err)
		return c.JSON(500, map[string]string{"error": "Failed to reset"})
	}

	return c.JSON(200, map[string]string{"status": "ok"})
}

func (s *Server) handleRotateOverlayUUID(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return c.String(500, "Internal error: invalid user ID")
	}
	ctx := c.Request().Context()

	newUUID, err := s.app.RotateOverlayUUID(ctx, userID)
	if err != nil {
		log.Printf("Failed to rotate overlay UUID: %v", err)
		return c.JSON(500, map[string]string{"error": "Failed to rotate URL"})
	}

	newURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), newUUID)
	return c.JSON(200, map[string]any{
		"status":   "ok",
		"new_uuid": newUUID.String(),
		"new_url":  newURL,
	})
}
