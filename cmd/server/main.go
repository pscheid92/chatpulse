package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
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
	"github.com/pscheid92/chatpulse/internal/logging"
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
		slog.Error("Failed to create EventSub manager", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := eventsubManager.Setup(ctx); err != nil {
		slog.Error("Failed to setup webhook conduit", "error", err)
		os.Exit(1)
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
		slog.Info("Shutdown signal received, cleaning up...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("Server shutdown error", "error", err)
		}

		appSvc.Stop()
		broadcaster.Stop()

		if conduitMgr != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := conduitMgr.Cleanup(shutdownCtx); err != nil {
				slog.Error("Failed to clean up conduit", "error", err)
			}
		}

		close(done)
	}()

	return done
}

func setupConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		// Use log before slog is initialized
		log.Fatalf("Failed to load config: %v", err)
	}
	return cfg
}

func setupDB(cfg *config.Config) *pgxpool.Pool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}

	if err := database.RunMigrations(ctx, db); err != nil {
		slog.Error("Failed to run migrations", "error", err)
		os.Exit(1)
	}

	return db
}

func setupRedis(ctx context.Context, cfg *config.Config) *goredis.Client {
	client, err := redis.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	return client
}

func main() {
	clock := clockwork.NewRealClock()

	cfg := setupConfig()

	// Initialize structured logging
	logging.InitLogger(cfg.LogLevel, cfg.LogFormat)
	slog.Info("Application starting", "env", cfg.AppEnv, "port", cfg.Port)

	pool := setupDB(cfg)
	defer pool.Close()

	redisClient := setupRedis(context.Background(), cfg)
	defer func() { _ = redisClient.Close() }()

	store := redis.NewSessionRepo(redisClient, clock)
	sentimentStore := redis.NewSentimentStore(redisClient)
	debouncer := redis.NewDebouncer(redisClient)

	// Create config cache with 10-second TTL
	configCache := sentiment.NewConfigCache(10*time.Second, clock)
	stopEviction := configCache.StartEvictionTimer(1 * time.Minute)
	defer stopEviction()

	engine := sentiment.NewEngine(store, sentimentStore, debouncer, clock, configCache)

	// Construct repositories
	var cryptoSvc crypto.Service = crypto.NoopService{}
	if cfg.TokenEncryptionKey != "" {
		var err error
		cryptoSvc, err = crypto.NewAesGcmCryptoService(cfg.TokenEncryptionKey)
		if err != nil {
			slog.Error("Failed to create crypto service", "error", err)
			os.Exit(1)
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
			slog.Error("Failed to increment ref count", "session_uuid", sessionUUID.String(), "error", err)
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
		srv, srvErr = server.NewServer(cfg, appSvc, broadcaster, webhookHdlr, pool, redisClient)
	} else {
		srv, srvErr = server.NewServer(cfg, appSvc, broadcaster, nil, pool, redisClient)
	}
	if srvErr != nil {
		slog.Error("Failed to create server", "error", srvErr)
		os.Exit(1)
	}

	done := runGracefulShutdown(srv, appSvc, broadcaster, eventsubMgr)

	slog.Info("Server starting", "port", cfg.Port)
	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}

	<-done
}
