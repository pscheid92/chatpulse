package server

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/pscheid92/chatpulse/internal/websocket"
)

const (
	sessionMaxAgeDays = 7
	cleanupInterval   = 30 * time.Second
)

// dataStore is the subset of database operations used by the server
type dataStore interface {
	GetUserByID(ctx context.Context, userID uuid.UUID) (*models.User, error)
	GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*models.User, error)
	GetConfig(ctx context.Context, userID uuid.UUID) (*models.Config, error)
	UpdateConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
	UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*models.User, error)
	RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

// sentimentService is the subset of sentiment engine operations used by the server
type sentimentService interface {
	ActivateSession(ctx context.Context, sessionUUID uuid.UUID, userID uuid.UUID, broadcasterUserID string) error
	ResetSession(sessionUUID uuid.UUID)
	MarkDisconnected(sessionUUID uuid.UUID)
	CleanupOrphans(twitchManager sentiment.TwitchManager)
	UpdateSessionConfig(sessionUUID uuid.UUID, snapshot models.ConfigSnapshot)
}

// twitchService is the subset of Twitch operations used by the server
type twitchService interface {
	Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error
	Unsubscribe(ctx context.Context, userID uuid.UUID) error
}

// webhookHandler handles EventSub webhook requests (nil if webhooks not configured)
type webhookHandler interface {
	HandleEventSub(c echo.Context) error
}

type Server struct {
	echo              *echo.Echo
	config            *config.Config
	db                dataStore
	sentiment         sentimentService
	twitch            twitchService
	hub               *websocket.Hub
	webhook           webhookHandler
	sessionStore      *sessions.CookieStore
	cleanupTimer      *time.Ticker
	cleanupStopCh     chan struct{}
	loginTemplate     *template.Template
	dashboardTemplate *template.Template
	overlayTemplate   *template.Template
}

func NewServer(cfg *config.Config, db dataStore, sentiment sentimentService, twitch twitchService, hub *websocket.Hub, webhook webhookHandler) (*Server, error) {
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
		db:                db,
		sentiment:         sentiment,
		twitch:            twitch,
		hub:               hub,
		webhook:           webhook,
		sessionStore:      sessionStore,
		cleanupStopCh:     make(chan struct{}),
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashboardTmpl,
		overlayTemplate:   overlayTmpl,
	}

	// Register routes
	srv.registerRoutes()

	// Start cleanup timer (30s interval for orphaned sessions)
	srv.startCleanupTimer()

	return srv, nil
}

func (s *Server) Start() error {
	return s.echo.Start(fmt.Sprintf(":%s", s.config.Port))
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Stop cleanup timer
	if s.cleanupTimer != nil {
		s.cleanupTimer.Stop()
		close(s.cleanupStopCh)
	}

	return s.echo.Shutdown(ctx)
}

func (s *Server) startCleanupTimer() {
	s.cleanupTimer = time.NewTicker(cleanupInterval)
	go func() {
		for {
			select {
			case <-s.cleanupTimer.C:
				if s.twitch != nil {
					s.sentiment.CleanupOrphans(s.twitch)
				}
			case <-s.cleanupStopCh:
				return
			}
		}
	}()
	log.Println("Cleanup timer started (30s interval)")
}
