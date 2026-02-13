package server

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/sessions"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo-contrib/echoprometheus"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/chatpulse/internal/broadcast"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
	goredis "github.com/redis/go-redis/v9"
)

// findProjectRoot walks up the directory tree to find the project root (where go.mod is located).
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding go.mod
			return "", fmt.Errorf("could not find project root (go.mod)")
		}
		dir = parent
	}
}

// getTemplatePath returns the absolute path to a template file.
func getTemplatePath(relativePath string) (string, error) {
	// Try relative path first (works when running from project root)
	if _, err := os.Stat(relativePath); err == nil {
		return relativePath, nil
	}

	// Find project root and construct absolute path
	root, err := findProjectRoot()
	if err != nil {
		return "", err
	}

	return filepath.Join(root, relativePath), nil
}

// webhookHandler handles EventSub webhook requests (nil if webhooks not configured)
type webhookHandler interface {
	HandleEventSub(c echo.Context) error
}

type Server struct {
	echo              *echo.Echo
	config            *config.Config
	app               domain.AppService
	broadcaster       *broadcast.Broadcaster
	webhook           webhookHandler
	oauthClient       twitchOAuthClient
	sessionStore      *sessions.CookieStore
	csrfMiddleware    echo.MiddlewareFunc
	loginTemplate     *template.Template
	dashboardTemplate *template.Template
	overlayTemplate   *template.Template
	db                *pgxpool.Pool
	redisClient       *goredis.Client
	twitchService     domain.TwitchService
	connLimits        *ConnectionLimits
	startTime         time.Time
	// For testing only - allows injecting mock health checkers
	redisHealthCheck    redisHealthChecker
	postgresHealthCheck postgresHealthChecker
}

func NewServer(cfg *config.Config, app domain.AppService, broadcaster *broadcast.Broadcaster, webhook webhookHandler, twitchService domain.TwitchService, db *pgxpool.Pool, redisClient *goredis.Client) (*Server, error) {
	// Parse templates once at startup
	loginPath, err := getTemplatePath("web/templates/login.html")
	if err != nil {
		return nil, fmt.Errorf("failed to find login template: %w", err)
	}
	loginTmpl, err := template.ParseFiles(loginPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse login template: %w", err)
	}

	dashboardPath, err := getTemplatePath("web/templates/dashboard.html")
	if err != nil {
		return nil, fmt.Errorf("failed to find dashboard template: %w", err)
	}
	dashboardTmpl, err := template.ParseFiles(dashboardPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse dashboard template: %w", err)
	}

	overlayPath, err := getTemplatePath("web/templates/overlay.html")
	if err != nil {
		return nil, fmt.Errorf("failed to find overlay template: %w", err)
	}
	overlayTmpl, err := template.ParseFiles(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse overlay template: %w", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Middleware
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
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
	}))
	e.Use(middleware.Recover())
	e.Use(apperrors.Middleware())                    // Structured error handling
	e.Use(echoprometheus.NewMiddleware("chatpulse")) // Metrics endpoint at /metrics

	// Session store
	sessionStore := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	sessionStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   int(cfg.SessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
	}

	// Configure CSRF middleware for authenticated routes
	csrfMiddleware := middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup:    "form:csrf_token,header:X-CSRF-Token",
		CookieName:     "csrf_token",
		CookiePath:     "/",
		CookieMaxAge:   int(cfg.SessionMaxAge.Seconds()),
		CookieHTTPOnly: true,
		CookieSecure:   cfg.AppEnv == "production",
		CookieSameSite: http.SameSiteStrictMode,
	})

	// Initialize connection limiter
	connLimits := NewConnectionLimits(
		int64(cfg.MaxWebSocketConnections),
		cfg.MaxConnectionsPerIP,
		cfg.ConnectionRatePerIP,
		cfg.ConnectionRateBurst,
	)
	slog.Info("Connection limits initialized",
		"max_global", cfg.MaxWebSocketConnections,
		"max_per_ip", cfg.MaxConnectionsPerIP,
		"rate_per_ip", cfg.ConnectionRatePerIP,
		"rate_burst", cfg.ConnectionRateBurst)

	srv := &Server{
		echo:              e,
		config:            cfg,
		app:               app,
		broadcaster:       broadcaster,
		webhook:           webhook,
		twitchService:     twitchService,
		oauthClient:       newTwitchOAuthClient(cfg.TwitchClientID, cfg.TwitchClientSecret, cfg.TwitchRedirectURI),
		sessionStore:      sessionStore,
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashboardTmpl,
		overlayTemplate:   overlayTmpl,
		csrfMiddleware:    csrfMiddleware,
		db:                db,
		redisClient:       redisClient,
		connLimits:        connLimits,
		startTime:         time.Now(),
	}

	// Register routes
	srv.registerRoutes()

	return srv, nil
}

func (s *Server) Start() error {
	slog.Info("Starting server", "port", s.config.Port)
	if err := s.echo.Start(fmt.Sprintf(":%s", s.config.Port)); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.echo.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown server: %w", err)
	}
	return nil
}
