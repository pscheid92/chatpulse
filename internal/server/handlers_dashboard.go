package server

import (
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/models"
)

const (
	maxTriggerLen = 500
	maxLabelLen   = 50
	minDecaySpeed = 0.1
	maxDecaySpeed = 2.0
)

func validateConfig(forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	if strings.TrimSpace(forTrigger) == "" {
		return fmt.Errorf("for trigger cannot be empty")
	}
	if strings.TrimSpace(againstTrigger) == "" {
		return fmt.Errorf("against trigger cannot be empty")
	}

	if strings.EqualFold(strings.TrimSpace(forTrigger), strings.TrimSpace(againstTrigger)) {
		return fmt.Errorf("for trigger and against trigger must be different")
	}

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

	if decaySpeed < minDecaySpeed || decaySpeed > maxDecaySpeed {
		return fmt.Errorf("decay speed must be between %.1f and %.1f", minDecaySpeed, maxDecaySpeed)
	}

	return nil
}

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

func (s *Server) handleSaveConfig(c echo.Context) error {
	userID, ok := c.Get("userID").(uuid.UUID)
	if !ok {
		return c.String(500, "Internal error: invalid user ID")
	}
	ctx := c.Request().Context()

	forTrigger := c.FormValue("for_trigger")
	againstTrigger := c.FormValue("against_trigger")
	leftLabel := c.FormValue("left_label")
	rightLabel := c.FormValue("right_label")

	var decaySpeed float64
	if _, err := fmt.Sscanf(c.FormValue("decay_speed"), "%f", &decaySpeed); err != nil {
		return c.String(400, "Invalid decay speed")
	}

	if err := validateConfig(forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed); err != nil {
		return c.String(400, fmt.Sprintf("Validation error: %v", err))
	}

	if err := s.db.UpdateConfig(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed); err != nil {
		log.Printf("Failed to update config: %v", err)
		return c.String(500, "Failed to save config")
	}

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
