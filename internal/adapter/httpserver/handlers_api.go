package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/platform/errors"
	"github.com/pscheid92/uuid"
)

func (s *Server) registerAPIRoutes(csrfMiddleware echo.MiddlewareFunc) {
	s.echo.POST("/api/reset/:uuid", s.handleResetSentiment, s.requireAuth, csrfMiddleware)
	s.echo.POST("/api/rotate-overlay-uuid", s.handleRotateOverlayUUID, s.requireAuth, csrfMiddleware)
	s.echo.GET("/api/overlay-status", s.handleOverlayStatus, s.requireAuth, csrfMiddleware)
}

func (s *Server) handleResetSentiment(c echo.Context) error {
	ctx := c.Request().Context()

	streamerID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid streamer ID in context", nil)
	}

	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return apperrors.ValidationError("invalid UUID format").WithField("uuid", overlayUUIDStr)
	}

	streamer, err := s.app.GetStreamerByID(ctx, streamerID)
	if errors.Is(err, domain.ErrStreamerNotFound) {
		return apperrors.NotFoundError("streamer not found").WithField("streamer_id", streamerID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to load streamer", err).WithField("streamer_id", streamerID.String())
	}

	if streamer.OverlayUUID != overlayUUID {
		return apperrors.ValidationError("forbidden: UUID does not match streamer's overlay UUID").
			WithField("streamer_id", streamerID.String()).
			WithField("provided_uuid", overlayUUID.String()).
			WithField("expected_uuid", streamer.OverlayUUID.String())
	}

	if err := s.app.ResetSentiment(ctx, overlayUUID); err != nil {
		slog.Error("Failed to reset sentiment", "error", err, "overlay_uuid", overlayUUID)
		return apperrors.InternalError("failed to reset sentiment", err).WithField("overlay_uuid", overlayUUID.String())
	}

	if err := c.JSON(http.StatusOK, map[string]string{"status": "ok"}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}

func (s *Server) handleOverlayStatus(c echo.Context) error {
	ctx := c.Request().Context()

	streamerID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid streamer ID in context", nil)
	}

	streamer, err := s.app.GetStreamerByID(ctx, streamerID)
	if errors.Is(err, domain.ErrStreamerNotFound) {
		return apperrors.NotFoundError("streamer not found").WithField("streamer_id", streamerID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to load streamer", err).WithField("streamer_id", streamerID.String())
	}

	viewers := s.presence.ViewerCount(streamer.TwitchUserID)
	if err := c.JSON(http.StatusOK, map[string]int{"viewers": viewers}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}

func (s *Server) handleRotateOverlayUUID(c echo.Context) error {
	ctx := c.Request().Context()

	streamerID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid streamer ID in context", nil)
	}

	newUUID, err := s.app.RotateOverlayUUID(ctx, streamerID)
	if errors.Is(err, domain.ErrStreamerNotFound) {
		return apperrors.NotFoundError("streamer not found").WithField("streamer_id", streamerID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to rotate overlay UUID", err).WithField("streamer_id", streamerID.String())
	}

	newURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), newUUID)
	response := map[string]string{
		"status":   "ok",
		"new_uuid": newUUID.String(),
		"new_url":  newURL,
	}
	if err := c.JSON(http.StatusOK, response); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}
