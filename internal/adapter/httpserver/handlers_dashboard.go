package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/platform/errors"
	"github.com/pscheid92/uuid"
)

const (
	maxTriggerLen         = 500
	maxLabelLen           = 50
	minMemorySeconds      = 5
	maxMemorySeconds      = 120
	infiniteMemorySeconds = 43200 // 12 hours — used when slider is set to "infinite"
)

func (s *Server) registerDashboardRoutes(csrfMiddleware, rateLimiter echo.MiddlewareFunc) {
	s.echo.GET("/dashboard", s.handleDashboard, rateLimiter, s.requireAuth, csrfMiddleware)
	s.echo.POST("/dashboard/config", s.handleSaveConfig, rateLimiter, s.requireAuth, csrfMiddleware)
}

func validateConfig(forTrigger, forLabel, againstTrigger, againstLabel string, memorySeconds int, displayMode, theme string) error {
	// Trim whitespace before validation
	forTrigger = strings.TrimSpace(forTrigger)
	againstTrigger = strings.TrimSpace(againstTrigger)
	forLabel = strings.TrimSpace(forLabel)
	againstLabel = strings.TrimSpace(againstLabel)

	// Check emptiness (after trimming)
	if forTrigger == "" {
		return errors.New("for trigger cannot be empty")
	}
	if againstTrigger == "" {
		return errors.New("against trigger cannot be empty")
	}

	// Check for identical triggers (case-insensitive)
	if strings.EqualFold(forTrigger, againstTrigger) {
		return errors.New("for trigger and against trigger must be different")
	}

	// Check length limits (after trimming)
	if len(forTrigger) > maxTriggerLen {
		return fmt.Errorf("for trigger exceeds %d characters", maxTriggerLen)
	}
	if len(againstTrigger) > maxTriggerLen {
		return fmt.Errorf("against trigger exceeds %d characters", maxTriggerLen)
	}
	if len(againstLabel) > maxLabelLen {
		return fmt.Errorf("against label exceeds %d characters", maxLabelLen)
	}
	if len(forLabel) > maxLabelLen {
		return fmt.Errorf("for label exceeds %d characters", maxLabelLen)
	}

	// Check memory seconds range (also accept infiniteMemorySeconds for "infinite" mode)
	if memorySeconds != infiniteMemorySeconds && (memorySeconds < minMemorySeconds || memorySeconds > maxMemorySeconds) {
		return fmt.Errorf("memory seconds must be between %d and %d", minMemorySeconds, maxMemorySeconds)
	}

	// Check display mode
	if displayMode != "combined" && displayMode != "split" {
		return errors.New("display mode must be 'combined' or 'split'")
	}

	// Check theme
	if theme != "dark" && theme != "light" {
		return errors.New("theme must be 'dark' or 'light'")
	}

	return nil
}

func (s *Server) handleDashboard(c echo.Context) error {
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

	config, err := s.app.GetConfig(ctx, streamerID)
	if errors.Is(err, domain.ErrConfigNotFound) {
		return apperrors.NotFoundError("config not found").WithField("streamer_id", streamerID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to load config", err).WithField("streamer_id", streamerID.String())
	}

	overlayURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), streamer.OverlayUUID)

	data := map[string]any{
		"Username":       streamer.TwitchUsername,
		"OverlayURL":     overlayURL,
		"OverlayUUID":    streamer.OverlayUUID.String(),
		"ForTrigger":     config.ForTrigger,
		"AgainstTrigger": config.AgainstTrigger,
		"AgainstLabel":   config.AgainstLabel,
		"ForLabel":       config.ForLabel,
		"MemorySeconds":  config.MemorySeconds,
		"DisplayMode":    string(config.DisplayMode),
		"Theme":          string(config.Theme),
		"CSRFToken":      c.Get("csrf"),
	}

	return s.renderTemplate(c, "dashboard.html", data)
}

func (s *Server) handleSaveConfig(c echo.Context) error {
	ctx := c.Request().Context()

	streamerID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid streamer ID in context", nil)
	}

	// Trim whitespace from form values before validation and storage
	forTrigger := strings.TrimSpace(c.FormValue("for_trigger"))
	forLabel := strings.TrimSpace(c.FormValue("for_label"))
	againstTrigger := strings.TrimSpace(c.FormValue("against_trigger"))
	againstLabel := strings.TrimSpace(c.FormValue("against_label"))
	displayMode := strings.TrimSpace(c.FormValue("display_mode"))
	theme := strings.TrimSpace(c.FormValue("theme"))

	memorySeconds, err := strconv.Atoi(c.FormValue("memory_seconds"))
	if err != nil {
		return apperrors.ValidationError("invalid memory seconds format").WithField("memory_seconds", c.FormValue("memory_seconds"))
	}

	if err := validateConfig(forTrigger, forLabel, againstTrigger, againstLabel, memorySeconds, displayMode, theme); err != nil {
		return apperrors.ValidationError(err.Error()).
			WithField("for_trigger", forTrigger).
			WithField("against_trigger", againstTrigger).
			WithField("against_label", againstLabel).
			WithField("for_label", forLabel).
			WithField("memory_seconds", memorySeconds).
			WithField("display_mode", displayMode).
			WithField("theme", theme)
	}

	streamer, err := s.app.GetStreamerByID(ctx, streamerID)
	if errors.Is(err, domain.ErrStreamerNotFound) {
		return apperrors.NotFoundError("streamer not found").WithField("streamer_id", streamerID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to get streamer", err).WithField("streamer_id", streamerID.String())
	}

	request := app.SaveConfigRequest{
		StreamerID:     streamerID,
		ForTrigger:     forTrigger,
		AgainstTrigger: againstTrigger,
		AgainstLabel:   againstLabel,
		ForLabel:       forLabel,
		MemorySeconds:  memorySeconds,
		DisplayMode:    displayMode,
		Theme:          theme,
		BroadcasterID:  streamer.TwitchUserID,
	}
	if err := s.app.SaveConfig(ctx, request); err != nil {
		return apperrors.InternalError("failed to save config", err).
			WithField("streamer_id", streamerID.String()).
			WithField("broadcaster_id", streamer.TwitchUserID)
	}

	slog.InfoContext(ctx, "Config updated", "streamer_id", streamerID, "broadcaster_id", streamer.TwitchUserID)

	// AJAX requests get a 204; browser form submissions get a redirect
	if c.Request().Header.Get("X-Requested-With") == "XMLHttpRequest" {
		if err := c.NoContent(http.StatusNoContent); err != nil {
			return fmt.Errorf("failed to send no-content response: %w", err)
		}
		return nil
	}

	if err := c.Redirect(http.StatusFound, "/dashboard"); err != nil {
		return fmt.Errorf("failed to redirect: %w", err)
	}
	return nil
}
