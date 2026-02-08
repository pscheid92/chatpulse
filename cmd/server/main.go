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
	"github.com/joho/godotenv"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/database"
	"github.com/pscheid92/chatpulse/internal/redis"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/pscheid92/chatpulse/internal/server"
	"github.com/pscheid92/chatpulse/internal/twitch"
	"github.com/pscheid92/chatpulse/internal/websocket"
)

type storeResult struct {
	store  sentiment.SessionStateStore
	client *redis.Client
	pubSub *redis.PubSub
}

type webhookResult struct {
	conduitManager      *twitch.ConduitManager
	subscriptionManager *twitch.SubscriptionManager
	webhookHandler      *twitch.WebhookHandler
}

func initStore(cfg *config.Config, clock clockwork.Clock) (*storeResult, error) {
	if cfg.RedisURL == "" {
		log.Println("Using in-memory session store (single-instance mode)")
		return &storeResult{store: sentiment.NewInMemoryStore(clock)}, nil
	}

	redisClient, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx); err != nil {
		return nil, err
	}
	log.Println("Connected to Redis")

	sessionStore := redis.NewSessionStore(redisClient)
	scriptRunner := redis.NewScriptRunner(redisClient)
	redisPubSub := redis.NewPubSub(redisClient)
	store := sentiment.NewRedisStore(sessionStore, scriptRunner, clock)

	return &storeResult{store: store, client: redisClient, pubSub: redisPubSub}, nil
}

func initWebhooks(cfg *config.Config, twitchClient *twitch.TwitchClient, store sentiment.SessionStateStore, db *database.DB) (*webhookResult, error) {
	if cfg.WebhookCallbackURL == "" || cfg.WebhookSecret == "" {
		return &webhookResult{}, nil
	}

	conduitManager := twitch.NewConduitManager(twitchClient, cfg.WebhookCallbackURL, cfg.WebhookSecret)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conduitID, err := conduitManager.Setup(ctx)
	if err != nil {
		return nil, err
	}

	subscriptionManager := twitch.NewSubscriptionManager(twitchClient, db, conduitID, cfg.BotUserID)
	webhookHandler := twitch.NewWebhookHandler(cfg.WebhookSecret, store)

	return &webhookResult{
		conduitManager:      conduitManager,
		subscriptionManager: subscriptionManager,
		webhookHandler:      webhookHandler,
	}, nil
}

func newHubCallbacks(db *database.DB, engine *sentiment.Engine, subMgr *twitch.SubscriptionManager) (func(uuid.UUID) error, func(uuid.UUID)) {
	onFirstConnect := func(sessionUUID uuid.UUID) error {
		ctx := context.Background()
		user, err := db.GetUserByOverlayUUID(ctx, sessionUUID)
		if err != nil {
			return err
		}
		if err := engine.ActivateSession(ctx, user.OverlayUUID, user.ID, user.TwitchUserID); err != nil {
			return err
		}
		if subMgr != nil {
			return subMgr.Subscribe(ctx, user.ID, user.TwitchUserID)
		}
		return nil
	}

	onLastDisconnect := func(sessionUUID uuid.UUID) {
		engine.MarkDisconnected(sessionUUID)
	}

	return onFirstConnect, onLastDisconnect
}

func runGracefulShutdown(srv *server.Server, engine *sentiment.Engine, hub *websocket.Hub, conduitMgr *twitch.ConduitManager) <-chan struct{} {
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

		engine.Stop()
		hub.Stop()

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

func main() {
	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Connect to database
	db, err := database.Connect(cfg.DatabaseURL, cfg.TokenEncryptionKey)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Run migrations
	if err := db.RunMigrations(); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Create Twitch client (gets app access token for conduit management)
	twitchClient, err := twitch.NewTwitchClient(cfg.TwitchClientID, cfg.TwitchClientSecret)
	if err != nil {
		log.Fatalf("Failed to create Twitch client: %v", err)
	}

	// Set up session state store (Redis or in-memory)
	clock := clockwork.NewRealClock()
	sr, err := initStore(cfg, clock)
	if err != nil {
		log.Fatalf("Failed to initialize store: %v", err)
	}
	if sr.client != nil {
		defer sr.client.Close()
	}

	// Create sentiment engine (hub will be wired up after creation)
	broadcastAfterDecay := sr.pubSub == nil // No pubsub = in-memory = need broadcast
	engine := sentiment.NewEngine(sr.store, db, clock, broadcastAfterDecay)

	// Set up conduit + webhook if configured
	wh, err := initWebhooks(cfg, twitchClient, sr.store, db)
	if err != nil {
		log.Fatalf("Failed to set up webhooks: %v", err)
	}

	// Create WebSocket hub with lifecycle callbacks
	onFirstConnect, onLastDisconnect := newHubCallbacks(db, engine, wh.subscriptionManager)
	hub := websocket.NewHub(onFirstConnect, onLastDisconnect)

	// Set up Redis Pub/Sub listener on Hub if Redis is available
	if sr.pubSub != nil {
		hub.SetPubSub(sr.pubSub)
	}

	// Wire up the broadcaster for the engine now that hub exists
	engine.SetBroadcaster(hub)

	// Start background actors
	engine.Start()

	// Create and start the HTTP server
	srv, err := server.NewServer(cfg, db, engine, wh.subscriptionManager, hub, wh.webhookHandler)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Handle graceful shutdown
	done := runGracefulShutdown(srv, engine, hub, wh.conduitManager)

	// Start the server (blocking until shutdown)
	log.Printf("Starting server on port %s", cfg.Port)
	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server error: %v", err)
	}

	<-done
}
