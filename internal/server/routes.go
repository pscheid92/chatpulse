package server

import (
	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (s *Server) registerRoutes() {
	// Observability endpoints (no auth required)
	s.echo.GET("/health/live", s.handleLiveness)
	s.echo.GET("/health/ready", s.handleReadiness)
	s.echo.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	// Root - redirect to dashboard
	s.echo.GET("/", func(c echo.Context) error {
		return c.Redirect(302, "/dashboard")
	})

	// Auth routes (logout requires CSRF, others don't)
	s.echo.GET("/auth/login", s.handleLoginPage)
	s.echo.GET("/auth/callback", s.handleOAuthCallback)
	s.echo.POST("/auth/logout", s.handleLogout, s.requireAuth, s.csrfMiddleware)

	// Dashboard (authenticated + CSRF protected)
	s.echo.GET("/dashboard", s.handleDashboard, s.requireAuth, s.csrfMiddleware)
	s.echo.POST("/dashboard/config", s.handleSaveConfig, s.requireAuth, s.csrfMiddleware)

	// API routes (authenticated + CSRF protected)
	s.echo.POST("/api/reset/:uuid", s.handleResetSentiment, s.requireAuth, s.csrfMiddleware)
	s.echo.POST("/api/rotate-overlay-uuid", s.handleRotateOverlayUUID, s.requireAuth, s.csrfMiddleware)

	// Webhook route (EventSub notifications from Twitch - NO CSRF)
	if s.webhook != nil {
		s.echo.POST("/webhooks/eventsub", s.webhook.HandleEventSub)
	}

	// Public routes (overlay and WebSocket - NO CSRF)
	s.echo.GET("/overlay/:uuid", s.handleOverlay)
	s.echo.GET("/ws/overlay/:uuid", s.handleWebSocket)
}
