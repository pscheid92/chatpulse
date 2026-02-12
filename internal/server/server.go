package server

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/gorilla/sessions"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo-contrib/echoprometheus"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/chatpulse/internal/broadcast"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

const sessionMaxAgeDays = 7

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
	connLimits        *ConnectionLimits
	// For testing only - allows injecting mock health checkers
	redisHealthCheck    redisHealthChecker
	postgresHealthCheck postgresHealthChecker
}

func NewServer(cfg *config.Config, app domain.AppService, broadcaster *broadcast.Broadcaster, webhook webhookHandler, db *pgxpool.Pool, redisClient *goredis.Client) (*Server, error) {
	// Parse templates once at startup
	loginTmpl, err := template.ParseFiles("web/templates/login.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse login template: %w", err)
	}
	dashboardTmpl, err := template.ParseFiles("web/templates/dashboard.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse dashboard template: %w", err)
	}
	overlayTmpl, err := template.ParseFiles("web/templates/overlay.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse overlay template: %w", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(echoprometheus.NewMiddleware("chatpulse")) // Metrics endpoint at /metrics

	// Session store
	sessionStore := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	sessionStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * sessionMaxAgeDays,
		HttpOnly: true,
		Secure:   cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
	}

	// Configure CSRF middleware for authenticated routes
	csrfMiddleware := middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup:    "form:csrf_token,header:X-CSRF-Token",
		CookieName:     "csrf_token",
		CookiePath:     "/",
		CookieMaxAge:   86400 * sessionMaxAgeDays,
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
		oauthClient:       newTwitchOAuthClient(cfg.TwitchClientID, cfg.TwitchClientSecret, cfg.TwitchRedirectURI),
		sessionStore:      sessionStore,
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashboardTmpl,
		overlayTemplate:   overlayTmpl,
		csrfMiddleware:    csrfMiddleware,
		db:                db,
		redisClient:       redisClient,
		connLimits:        connLimits,
	}

	// Register routes
	srv.registerRoutes()

	return srv, nil
}

func (s *Server) Start() error {
	slog.Info("Starting server", "port", s.config.Port)
	return s.echo.Start(fmt.Sprintf(":%s", s.config.Port))
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}
