package server

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/twitch-tow/internal/config"
	"github.com/pscheid92/twitch-tow/internal/models"
	"github.com/pscheid92/twitch-tow/internal/sentiment"
	"github.com/pscheid92/twitch-tow/internal/websocket"
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
	Subscribe(userID uuid.UUID, broadcasterUserID string) error
	Unsubscribe(userID uuid.UUID) error
	IsReconnecting() bool
}

type Server struct {
	echo            *echo.Echo
	config          *config.Config
	db              dataStore
	sentiment       sentimentService
	twitch          twitchService
	hub             *websocket.Hub
	sessionStore    *sessions.CookieStore
	cleanupTimer    *time.Ticker
	cleanupStopCh   chan struct{}
}

func NewServer(cfg *config.Config, db dataStore, sentiment sentimentService, twitch twitchService, hub *websocket.Hub) *Server {
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
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		Secure:   cfg.AppEnv == "production",
		SameSite: 2, // Lax
	}

	srv := &Server{
		echo:          e,
		config:        cfg,
		db:            db,
		sentiment:     sentiment,
		twitch:        twitch,
		hub:           hub,
		sessionStore:  sessionStore,
		cleanupStopCh: make(chan struct{}),
	}

	// Register routes
	srv.registerRoutes()

	// Start cleanup timer (30s interval for orphaned sessions)
	srv.startCleanupTimer()

	return srv
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
	s.cleanupTimer = time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-s.cleanupTimer.C:
				s.sentiment.CleanupOrphans(s.twitch)
			case <-s.cleanupStopCh:
				return
			}
		}
	}()
	log.Println("Cleanup timer started (30s interval)")
}
