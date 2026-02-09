package server

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/websocket"
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
	broadcaster       *websocket.OverlayBroadcaster
	webhook           webhookHandler
	oauthClient       twitchOAuthClient
	sessionStore      *sessions.CookieStore
	loginTemplate     *template.Template
	dashboardTemplate *template.Template
	overlayTemplate   *template.Template
}

func NewServer(cfg *config.Config, app domain.AppService, broadcaster *websocket.OverlayBroadcaster, webhook webhookHandler) (*Server, error) {
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

	// Session store
	sessionStore := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	sessionStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * sessionMaxAgeDays,
		HttpOnly: true,
		Secure:   cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
	}

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
	}

	// Register routes
	srv.registerRoutes()

	return srv, nil
}

func (s *Server) Start() error {
	log.Printf("Starting server on port %s", s.config.Port)
	return s.echo.Start(fmt.Sprintf(":%s", s.config.Port))
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}
