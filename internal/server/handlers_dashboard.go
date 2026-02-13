package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
)

const (
	maxTriggerLen = 500
	maxLabelLen   = 50
	minDecaySpeed = 0.1
	maxDecaySpeed = 2.0
)

func validateConfig(forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	// Trim whitespace before validation
	forTrigger = strings.TrimSpace(forTrigger)
	againstTrigger = strings.TrimSpace(againstTrigger)
	leftLabel = strings.TrimSpace(leftLabel)
	rightLabel = strings.TrimSpace(rightLabel)

	// Check emptiness (after trimming)
	if forTrigger == "" {
		return fmt.Errorf("for trigger cannot be empty")
	}
	if againstTrigger == "" {
		return fmt.Errorf("against trigger cannot be empty")
	}

	// Check for identical triggers (case-insensitive)
	if strings.EqualFold(forTrigger, againstTrigger) {
		return fmt.Errorf("for trigger and against trigger must be different")
	}

	// Check length limits (after trimming)
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

	// Check decay speed range
	if decaySpeed < minDecaySpeed || decaySpeed > maxDecaySpeed {
		return fmt.Errorf("decay speed must be between %.1f and %.1f", minDecaySpeed, maxDecaySpeed)
	}

	return nil
}

func (s *Server) handleDashboard(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid user ID in context", nil)
	}
	ctx := c.Request().Context()

	user, err := s.app.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return apperrors.NotFoundError("user not found").WithField("user_id", userID.String())
		}
		return apperrors.InternalError("failed to load user", err).WithField("user_id", userID.String())
	}

	config, err := s.app.GetConfig(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrConfigNotFound) {
			return apperrors.NotFoundError("config not found").WithField("user_id", userID.String())
		}
		return apperrors.InternalError("failed to load config", err).WithField("user_id", userID.String())
	}

	overlayURL := fmt.Sprintf("%s/overlay/%s", s.getBaseURL(c), user.OverlayUUID)

	data := map[string]any{
		"Username":       user.TwitchUsername,
		"OverlayURL":     overlayURL,
		"OverlayUUID":    user.OverlayUUID.String(),
		"ForTrigger":     config.ForTrigger,
		"AgainstTrigger": config.AgainstTrigger,
		"LeftLabel":      config.LeftLabel,
		"RightLabel":     config.RightLabel,
		"DecaySpeed":     config.DecaySpeed,
		"CSRFToken":      c.Get("csrf"),
	}

	return renderTemplate(c, s.dashboardTemplate, data)
}

func (s *Server) handleSaveConfig(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return apperrors.InternalError("invalid user ID in context", nil)
	}
	ctx := c.Request().Context()

	// Trim whitespace from form values before validation and storage
	forTrigger := strings.TrimSpace(c.FormValue("for_trigger"))
	againstTrigger := strings.TrimSpace(c.FormValue("against_trigger"))
	leftLabel := strings.TrimSpace(c.FormValue("left_label"))
	rightLabel := strings.TrimSpace(c.FormValue("right_label"))

	var decaySpeed float64
	if _, err := fmt.Sscanf(c.FormValue("decay_speed"), "%f", &decaySpeed); err != nil {
		return apperrors.ValidationError("invalid decay speed format").
			WithField("decay_speed", c.FormValue("decay_speed"))
	}

	if err := validateConfig(forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed); err != nil {
		return apperrors.ValidationError(err.Error()).
			WithField("for_trigger", forTrigger).
			WithField("against_trigger", againstTrigger).
			WithField("left_label", leftLabel).
			WithField("right_label", rightLabel).
			WithField("decay_speed", decaySpeed)
	}

	user, err := s.app.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return apperrors.NotFoundError("user not found").WithField("user_id", userID.String())
		}
		return apperrors.InternalError("failed to get user", err).WithField("user_id", userID.String())
	}

	if err := s.app.SaveConfig(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed, user.OverlayUUID); err != nil {
		return apperrors.InternalError("failed to save config", err).
			WithField("user_id", userID.String()).
			WithField("overlay_uuid", user.OverlayUUID.String())
	}

	if err := c.Redirect(302, "/dashboard"); err != nil {
		return fmt.Errorf("failed to redirect: %w", err)
	}
	return nil
}
