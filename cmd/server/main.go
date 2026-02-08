package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/pscheid92/twitch-tow/internal/config"
	"github.com/pscheid92/twitch-tow/internal/database"
	"github.com/pscheid92/twitch-tow/internal/server"
	"github.com/pscheid92/twitch-tow/internal/sentiment"
	"github.com/pscheid92/twitch-tow/internal/twitch"
	"github.com/pscheid92/twitch-tow/internal/websocket"
)

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

	// Create Helix client for Twitch API
	helixClient, err := twitch.NewHelixClient(db, cfg.TwitchClientID, cfg.TwitchClientSecret)
	if err != nil {
		log.Fatalf("Failed to create Helix client: %v", err)
	}

	// Create sentiment engine (hub will be wired up after creation)
	engine := sentiment.NewEngine(db, nil)

	// Create EventSub manager
	eventSubManager := twitch.NewEventSubManager(helixClient, engine)

	// Create WebSocket hub with lifecycle callbacks
	hub := websocket.NewHub(
		func(sessionUUID uuid.UUID) error {
			// onFirstConnect: activate session and subscribe to EventSub
			ctx := context.Background()
			user, err := db.GetUserByOverlayUUID(ctx, sessionUUID)
			if err != nil {
				return err
			}
			if err := engine.ActivateSession(ctx, user.OverlayUUID, user.ID, user.TwitchUserID); err != nil {
				return err
			}
			return eventSubManager.Subscribe(user.ID, user.TwitchUserID)
		},
		func(sessionUUID uuid.UUID) {
			// onLastDisconnect: mark session as disconnected
			engine.MarkDisconnected(sessionUUID)
		},
	)

	// Wire up the broadcaster for the engine now that hub exists
	engine.SetBroadcaster(hub)

	// Wire up the connection status function
	engine.SetStatusFunc(eventSubManager.GetConnectionStatus)

	// Start background actors
	engine.Start()

	// Create and start the HTTP server
	srv := server.NewServer(cfg, db, engine, eventSubManager, hub)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutdown signal received, cleaning up...")

		// Stop the HTTP server
		ctx := context.Background()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}

		// Stop background actors
		engine.Stop()
		eventSubManager.Shutdown()
		hub.Stop()

		os.Exit(0)
	}()

	// Start the server (blocking)
	log.Printf("Starting server on port %s", cfg.Port)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
