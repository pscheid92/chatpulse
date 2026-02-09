package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/database"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/redis"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/pscheid92/chatpulse/internal/server"
	"github.com/pscheid92/chatpulse/internal/twitch"
	"github.com/pscheid92/chatpulse/internal/websocket"
)

type webhookResult struct {
	eventsubManager *twitch.EventSubManager
	webhookHandler  *twitch.WebhookHandler
}

func initWebhooks(cfg *config.Config, twitchClient *twitch.TwitchClient, store domain.SessionStateStore, eventSubRepo domain.EventSubRepository) webhookResult {
	eventsubManager := twitch.NewEventSubManager(twitchClient, eventSubRepo, cfg.WebhookCallbackURL, cfg.WebhookSecret, cfg.BotUserID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := eventsubManager.Setup(ctx); err != nil {
		log.Fatalf("failed to setup webhook conduit: %v", err)
	}

	webhookHandler := twitch.NewWebhookHandler(cfg.WebhookSecret, store)

	return webhookResult{
		eventsubManager: eventsubManager,
		webhookHandler:  webhookHandler,
	}
}

func runGracefulShutdown(srv *server.Server, appSvc *app.Service, broadcaster *websocket.OverlayBroadcaster, conduitMgr *twitch.EventSubManager) <-chan struct{} {
	done := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutdown signal received, cleaning up...")

		ctx := context.Background()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}

		appSvc.Stop()
		broadcaster.Stop()

		if conduitMgr != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := conduitMgr.Cleanup(shutdownCtx); err != nil {
				log.Printf("Failed to clean up conduit: %v", err)
			}
		}

		close(done)
	}()

	return done
}

func setupConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	return cfg
}

func setupDB(cfg *config.Config) *database.DB {
	db, err := database.Connect(cfg.DatabaseURL, cfg.TokenEncryptionKey)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if err := db.RunMigrations(); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	return db
}

func setupTwitchClient(cfg *config.Config) *twitch.TwitchClient {
	twitchClient, err := twitch.NewTwitchClient(cfg.TwitchClientID, cfg.TwitchClientSecret)
	if err != nil {
		log.Fatalf("Failed to create Twitch client: %v", err)
	}
	return twitchClient
}

func setupRedis(cfg *config.Config) *redis.Client {
	client, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	return client
}

func main() {
	clock := clockwork.NewRealClock()

	cfg := setupConfig()
	db := setupDB(cfg)
	twitchClient := setupTwitchClient(cfg)

	redisClient := setupRedis(cfg)
	defer redisClient.Close()

	sessionStore := redis.NewSessionStore(redisClient)
	scriptRunner := redis.NewScriptRunner(redisClient)
	store := redis.NewSentimentStore(sessionStore, scriptRunner, clock)

	engine := sentiment.NewEngine(store, clock)

	// Construct repositories
	userRepo := database.NewUserRepo(db)
	configRepo := database.NewConfigRepo(db)
	eventSubRepo := database.NewEventSubRepo(db)

	wh := initWebhooks(cfg, twitchClient, store, eventSubRepo)

	appSvc := app.NewService(userRepo, configRepo, store, wh.eventsubManager, clock)

	onSessionEmpty := func(sessionUUID uuid.UUID) { appSvc.OnSessionEmpty(context.Background(), sessionUUID) }
	broadcaster := websocket.NewOverlayBroadcaster(engine, onSessionEmpty, clock)

	// Create and start the HTTP server
	srv, err := server.NewServer(cfg, appSvc, broadcaster, wh.webhookHandler)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	done := runGracefulShutdown(srv, appSvc, broadcaster, wh.eventsubManager)

	log.Printf("Starting server on port %s", cfg.Port)
	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server error: %v", err)
	}

	<-done
}
