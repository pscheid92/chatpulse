package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/adapter/twitch"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/config"
	"github.com/pscheid92/chatpulse/web"
	"github.com/pscheid92/uuid"
)

type appService interface {
	GetStreamerByID(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error)
	GetStreamerByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error)
	UpsertStreamer(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.Streamer, error)
	GetConfig(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error)
	SaveConfig(ctx context.Context, req app.SaveConfigRequest) error
	RotateOverlayUUID(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error)
	ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error
}

type Server struct {
	echo   *echo.Echo
	config *config.Config

	app           appService
	presence      twitch.ViewerPresence
	twitchService domain.EventSubService

	websocketHandler http.Handler
	webhookHandler   http.Handler

	templates *template.Template

	oauthClient  twitchOAuthClient
	sessionStore *sessions.CookieStore
	healthChecks []HealthCheck
	startTime    time.Time
}

func NewServer(cfg *config.Config, app appService, presence twitch.ViewerPresence, websocketHandler http.Handler, webhookHandler http.Handler, twitchService domain.EventSubService, healthChecks []HealthCheck) (*Server, error) {
	templates, err := template.ParseFS(web.TemplateFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	sessionStore := setupSessionSore(cfg)

	srv := &Server{
		echo:             e,
		config:           cfg,
		app:              app,
		presence:         presence,
		websocketHandler: websocketHandler,
		webhookHandler:   webhookHandler,
		twitchService:    twitchService,
		oauthClient:      newTwitchOAuthClient(cfg.TwitchClientID, cfg.TwitchClientSecret, cfg.TwitchRedirectURI),
		sessionStore:     sessionStore,
		templates:        templates,
		healthChecks:     healthChecks,
		startTime:        time.Now(),
	}

	srv.registerRoutes()

	return srv, nil
}

func (s *Server) Start() error {
	slog.Info("Starting server", "port", s.config.Port)
	if err := s.echo.Start(":" + s.config.Port); err != nil {
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

// Session keys
const (
	sessionName          = "chatpulse-session"
	sessionKeyToken      = "token"
	sessionKeyOAuthState = "oauth_state"
)

func (s *Server) renderTemplate(c echo.Context, name string, data any) error {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("Template execution failed", "path", c.Request().URL.Path, "error", err)
		if err := c.String(http.StatusInternalServerError, "Failed to render page"); err != nil {
			return fmt.Errorf("failed to send error response: %w", err)
		}
		return nil
	}
	if err := c.HTMLBlob(http.StatusOK, buf.Bytes()); err != nil {
		return fmt.Errorf("failed to send HTML response: %w", err)
	}
	return nil
}

func (s *Server) getBaseURL(c echo.Context) string {
	scheme := "http"
	if c.Request().TLS != nil {
		scheme = "https"
	}
	if fwdProto := c.Request().Header.Get("X-Forwarded-Proto"); fwdProto == "http" || fwdProto == "https" {
		scheme = fwdProto
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request().Host)
}

func setupSessionSore(cfg *config.Config) *sessions.CookieStore {
	sessionStore := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	sessionStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   int(cfg.SessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   cfg.AppEnv == "production",
		SameSite: http.SameSiteLaxMode,
	}
	return sessionStore
}
