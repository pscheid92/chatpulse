package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/platform/version"
)

const (
	startupProbeTimeout   = 2 * time.Second
	readinessProbeTimeout = 5 * time.Second
)

// HealthCheck is a named health check function.
type HealthCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

func (s *Server) registerHealthRoutes() {
	s.echo.GET("/health/startup", s.handleStartup)
	s.echo.GET("/health/live", s.handleLiveness)
	s.echo.GET("/health/ready", s.handleReadiness)
	s.echo.GET("/version", s.handleVersion)
}

func (s *Server) handleStartup(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), startupProbeTimeout)
	defer cancel()

	return s.runHealthChecks(c, ctx)
}

func (s *Server) handleLiveness(c echo.Context) error {
	uptime := time.Since(s.startTime).Seconds()

	response := map[string]any{
		"status": "ok",
		"uptime": uptime,
	}
	if err := c.JSON(http.StatusOK, response); err != nil {
		return fmt.Errorf("failed to write liveness response: %w", err)
	}

	return nil
}

func (s *Server) handleReadiness(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), readinessProbeTimeout)
	defer cancel()

	return s.runHealthChecks(c, ctx)
}

func (s *Server) runHealthChecks(c echo.Context, ctx context.Context) error {
	for _, hc := range s.healthChecks {
		err := hc.Check(ctx)
		if err == nil {
			// POSITIVE: no error found
			continue
		}

		response := map[string]any{
			"status":       "unhealthy",
			"failed_check": hc.Name,
			"error":        err.Error(),
		}
		if err := c.JSON(http.StatusServiceUnavailable, response); err != nil {
			return fmt.Errorf("failed to send JSON response: %w", err)
		}
		return nil
	}

	if err := c.JSON(http.StatusOK, map[string]string{"status": "ready"}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}

func (s *Server) handleVersion(c echo.Context) error {
	if err := c.JSON(http.StatusOK, version.Get()); err != nil {
		return fmt.Errorf("failed to write version response: %w", err)
	}
	return nil
}
