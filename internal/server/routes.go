package server

import (
	"github.com/labstack/echo/v4"
)

func (s *Server) registerRoutes() {
	// Root - redirect to dashboard
	s.echo.GET("/", func(c echo.Context) error {
		return c.Redirect(302, "/dashboard")
	})

	// Auth routes
	s.echo.GET("/auth/login", s.handleLoginPage)
	s.echo.GET("/auth/callback", s.handleOAuthCallback)
	s.echo.POST("/auth/logout", s.handleLogout)

	// Dashboard (authenticated)
	s.echo.GET("/dashboard", s.handleDashboard, s.requireAuth)
	s.echo.POST("/dashboard/config", s.handleSaveConfig, s.requireAuth)

	// API routes (authenticated)
	s.echo.POST("/api/reset/:uuid", s.handleResetSentiment, s.requireAuth)
	s.echo.POST("/api/rotate-overlay-uuid", s.handleRotateOverlayUUID, s.requireAuth)

	// Public routes (overlay and WebSocket)
	s.echo.GET("/overlay/:uuid", s.handleOverlay)
	s.echo.GET("/ws/overlay/:uuid", s.handleWebSocket)
}
