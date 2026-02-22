package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func (s *Server) registerRoutes() {
	s.echo.Use(correlationMiddleware)
	if s.httpMetrics != nil {
		s.echo.Use(s.httpMetrics.Middleware())
	}
	s.echo.Use(s.setupRequestLoggerMiddleware())
	s.echo.Use(middleware.Recover())
	s.echo.Use(ErrorHandlingMiddleware())
	s.echo.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:      "",
		ContentTypeNosniff: "nosniff",
		XFrameOptions:      "DENY",
		HSTSMaxAge:         63072000, // 2 years; only sent over HTTPS
		HSTSPreloadEnabled: true,
		ContentSecurityPolicy: "default-src 'self'; " +
			"script-src 'self' 'unsafe-inline'; " +
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
			"font-src 'self' https://fonts.gstatic.com; " +
			"frame-ancestors 'none'",
		ReferrerPolicy: "strict-origin-when-cross-origin",
	}))

	csrfMiddleware := s.setupCSRFMiddleware()

	authRL := newRateLimiter(0.17, 5)      // ~10 req/min, burst 5
	dashboardRL := newRateLimiter(0.5, 10) // ~30 req/min, burst 10
	apiRL := newRateLimiter(0.5, 10)       // ~30 req/min, burst 10
	webhookRL := newRateLimiter(3.33, 50)  // ~200 req/min, burst 50

	s.echo.GET("/", s.handleLanding)

	if s.metricsHandler != nil {
		s.echo.GET("/metrics", echo.WrapHandler(s.metricsHandler))
	}

	s.registerHealthRoutes()
	s.registerAuthRoutes(csrfMiddleware, authRL)
	s.registerDashboardRoutes(csrfMiddleware, dashboardRL)
	s.registerAPIRoutes(csrfMiddleware, apiRL)
	s.registerOverlayRoutes()

	s.echo.POST("/webhooks/eventsub", echo.WrapHandler(s.webhookHandler), webhookRL)
}

func (s *Server) setupRequestLoggerMiddleware() echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:  true,
		LogURI:     true,
		LogMethod:  true,
		LogLatency: true,
		LogError:   true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			attrs := []any{
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency", v.Latency,
			}
			if v.Error != nil {
				attrs = append(attrs, "error", v.Error)
			}
			slog.Info("Request", attrs...)
			return nil
		},
	})
}

func (s *Server) setupCSRFMiddleware() echo.MiddlewareFunc {
	secure := s.config.AppEnv == "production"
	maxAge := int(s.config.SessionMaxAge.Seconds())

	return middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup:    "form:csrf_token,header:X-CSRF-Token",
		CookieName:     "csrf_token",
		CookiePath:     "/",
		CookieMaxAge:   maxAge,
		CookieHTTPOnly: true,
		CookieSecure:   secure,
		CookieSameSite: http.SameSiteStrictMode,
	})
}
