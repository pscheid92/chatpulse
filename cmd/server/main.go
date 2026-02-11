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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/broadcast"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/crypto"
	"github.com/pscheid92/chatpulse/internal/database"
	"github.com/pscheid92/chatpulse/internal/domain"
	goredis "github.com/redis/go-redis/v9"

	"github.com/pscheid92/chatpulse/internal/redis"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/pscheid92/chatpulse/internal/server"
	"github.com/pscheid92/chatpulse/internal/twitch"
)

type webhookResult struct {
	eventsubManager *twitch.EventSubManager
	webhookHandler  *twitch.WebhookHandler
}

func initWebhooks(cfg *config.Config, engine domain.Engine, eventSubRepo domain.EventSubRepository) webhookResult {
	eventsubManager, err := twitch.NewEventSubManager(cfg.TwitchClientID, cfg.TwitchClientSecret, eventSubRepo, cfg.WebhookCallbackURL, cfg.WebhookSecret, cfg.BotUserID)
	if err != nil {
		log.Fatalf("Failed to create EventSub manager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := eventsubManager.Setup(ctx); err != nil {
		log.Fatalf("failed to setup webhook conduit: %v", err)
	}

	webhookHandler := twitch.NewWebhookHandler(cfg.WebhookSecret, engine)

	return webhookResult{
		eventsubManager: eventsubManager,
		webhookHandler:  webhookHandler,
	}
}

func runGracefulShutdown(srv *server.Server, appSvc *app.Service, broadcaster *broadcast.Broadcaster, conduitMgr *twitch.EventSubManager) <-chan struct{} {
	done := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutdown signal received, cleaning up...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
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

func setupDB(cfg *config.Config) *pgxpool.Pool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if err := database.RunMigrations(ctx, db); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	return db
}

func setupRedis(ctx context.Context, cfg *config.Config) *goredis.Client {
	client, err := redis.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	return client
}

func main() {
	clock := clockwork.NewRealClock()

	cfg := setupConfig()
	pool := setupDB(cfg)
	defer pool.Close()

	redisClient := setupRedis(context.Background(), cfg)
	defer func() { _ = redisClient.Close() }()

	store := redis.NewSessionRepo(redisClient, clock)
	sentimentStore := redis.NewSentimentStore(redisClient)
	debouncer := redis.NewDebouncer(redisClient)

	engine := sentiment.NewEngine(store, sentimentStore, debouncer, clock)

	// Construct repositories
	var cryptoSvc crypto.Service = crypto.NoopService{}
	if cfg.TokenEncryptionKey != "" {
		var err error
		cryptoSvc, err = crypto.NewAesGcmCryptoService(cfg.TokenEncryptionKey)
		if err != nil {
			log.Fatalf("Failed to create crypto service: %v", err)
		}
	}

	userRepo := database.NewUserRepo(pool, cryptoSvc)
	configRepo := database.NewConfigRepo(pool)
	eventSubRepo := database.NewEventSubRepo(pool)

	// Set up webhooks if configured (all three env vars required together)
	var twitchSvc domain.TwitchService
	var eventsubMgr *twitch.EventSubManager
	var webhookHdlr *twitch.WebhookHandler
	if cfg.WebhookCallbackURL != "" {
		wh := initWebhooks(cfg, engine, eventSubRepo)
		eventsubMgr = wh.eventsubManager
		webhookHdlr = wh.webhookHandler
		twitchSvc = eventsubMgr
	}

	appSvc := app.NewService(userRepo, configRepo, store, engine, twitchSvc, clock)

	onFirstClient := func(sessionUUID uuid.UUID) {
		if err := appSvc.IncrRefCount(context.Background(), sessionUUID); err != nil {
			log.Printf("Failed to increment ref count for session %s: %v", sessionUUID, err)
		}
	}
	onSessionEmpty := func(sessionUUID uuid.UUID) { appSvc.OnSessionEmpty(context.Background(), sessionUUID) }
	broadcaster := broadcast.NewBroadcaster(engine, onFirstClient, onSessionEmpty, clock)

	// Create and start the HTTP server (pass nil explicitly to avoid typed-nil interface)
	var (
		srv    *server.Server
		srvErr error
	)
	if webhookHdlr != nil {
		srv, srvErr = server.NewServer(cfg, appSvc, broadcaster, webhookHdlr)
	} else {
		srv, srvErr = server.NewServer(cfg, appSvc, broadcaster, nil)
	}
	if srvErr != nil {
		log.Fatalf("Failed to create server: %v", srvErr)
	}

	done := runGracefulShutdown(srv, appSvc, broadcaster, eventsubMgr)

	log.Printf("Starting server on port %s", cfg.Port)
	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server error: %v", err)
	}

	<-done
}
