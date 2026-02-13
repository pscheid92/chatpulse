package server

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
)

func (s *Server) handleResetSentiment(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid user ID in context", nil)
	}
	overlayUUIDStr := c.Param("uuid")

	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return apperrors.ValidationError("invalid UUID format").WithField("uuid", overlayUUIDStr)
	}

	ctx := c.Request().Context()

	user, err := s.app.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return apperrors.NotFoundError("user not found").WithField("user_id", userID.String())
		}
		return apperrors.InternalError("failed to load user", err).WithField("user_id", userID.String())
	}

	if user.OverlayUUID != overlayUUID {
		return apperrors.ValidationError("forbidden: UUID does not match user's overlay UUID").
			WithField("user_id", userID.String()).
			WithField("provided_uuid", overlayUUID.String()).
			WithField("expected_uuid", user.OverlayUUID.String())
	}

	if err := s.app.ResetSentiment(ctx, overlayUUID); err != nil {
		slog.Error("Failed to reset sentiment", "error", err, "overlay_uuid", overlayUUID)
		return apperrors.InternalError("failed to reset sentiment", err).
			WithField("overlay_uuid", overlayUUID.String())
	}

	if err := c.JSON(200, map[string]string{"status": "ok"}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}

func (s *Server) handleRotateOverlayUUID(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid user ID in context", nil)
	}
	ctx := c.Request().Context()

	newUUID, err := s.app.RotateOverlayUUID(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return apperrors.NotFoundError("user not found").WithField("user_id", userID.String())
		}
		return apperrors.InternalError("failed to rotate overlay UUID", err).
			WithField("user_id", userID.String())
	}

	newURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), newUUID)
	if err := c.JSON(200, map[string]any{
		"status":   "ok",
		"new_uuid": newUUID.String(),
		"new_url":  newURL,
	}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}
