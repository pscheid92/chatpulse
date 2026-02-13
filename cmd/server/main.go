package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/broadcast"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/coordination"
	"github.com/pscheid92/chatpulse/internal/crypto"
	"github.com/pscheid92/chatpulse/internal/database"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/logging"
	goredis "github.com/redis/go-redis/v9"

	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/pscheid92/chatpulse/internal/redis"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/pscheid92/chatpulse/internal/server"
	"github.com/pscheid92/chatpulse/internal/twitch"
	"github.com/pscheid92/chatpulse/internal/version"
)

type webhookResult struct {
	eventsubManager *twitch.EventSubManager
	webhookHandler  *twitch.WebhookHandler
}

func initWebhooks(cfg *config.Config, engine domain.Engine, eventSubRepo domain.EventSubRepository, hasViewers func(string) bool) (webhookResult, error) {
	eventsubManager, err := twitch.NewEventSubManager(cfg.TwitchClientID, cfg.TwitchClientSecret, eventSubRepo, cfg.WebhookCallbackURL, cfg.WebhookSecret, cfg.BotUserID)
	if err != nil {
		return webhookResult{}, fmt.Errorf("failed to create EventSub manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := eventsubManager.Setup(ctx); err != nil {
		return webhookResult{}, fmt.Errorf("failed to setup webhook conduit: %w", err)
	}

	webhookHandler := twitch.NewWebhookHandler(cfg.WebhookSecret, engine, hasViewers)

	return webhookResult{
		eventsubManager: eventsubManager,
		webhookHandler:  webhookHandler,
	}, nil
}

func runGracefulShutdown(srv *server.Server, broadcaster *broadcast.Broadcaster, conduitMgr *twitch.EventSubManager) <-chan struct{} {
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

func setupRedis(ctx context.Context, cfg *config.Config) *goredis.Client {
	client, err := redis.NewClient(ctx, cfg.RedisURL)
	if err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	return client
}

// checkUlimit verifies file descriptor limits are sufficient for WebSocket connections.
// Logs a warning if the limit is below recommended threshold.
func checkUlimit(maxConnections int) {
	var rlimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit); err != nil {
		slog.Warn("Failed to check file descriptor limit", "error", err)
		return
	}

	// Each WebSocket connection uses 1 FD, plus overhead for DB, Redis, HTTP, logs, etc.
	// Recommend 2x the max connections to ensure headroom
	recommended := uint64(maxConnections * 2)

	if rlimit.Cur < recommended {
		slog.Warn("File descriptor limit may be too low for WebSocket connections",
			"current", rlimit.Cur,
			"recommended", recommended,
			"max_connections", maxConnections,
			"note", "Consider running 'ulimit -n "+string(rune(recommended))+"' or updating system limits")
	} else {
		slog.Info("File descriptor limit check passed",
			"current", rlimit.Cur,
			"max_connections", maxConnections)
	}
}

func main() {
	clock := clockwork.NewRealClock()

	cfg := setupConfig()

	// Initialize structured logging
	logging.InitLogger(cfg.LogLevel, cfg.LogFormat)

	// Register build information metric
	buildInfo := version.Get()
	metrics.BuildInfo.WithLabelValues(
		buildInfo.Version,
		buildInfo.Commit,
		buildInfo.BuildTime,
		buildInfo.GoVersion,
	).Set(1)

	slog.Info("Application starting",
		"env", cfg.AppEnv,
		"port", cfg.Port,
		"version", buildInfo.Version,
		"commit", buildInfo.Commit,
		"build_time", buildInfo.BuildTime)

	// Check file descriptor limits for WebSocket connections
	checkUlimit(cfg.MaxWebSocketConnections)

	// Connect to database with retry logic (30s max)
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer dbCancel()
	poolCfg := database.PoolConfig{
		MinConns:          cfg.DBMinConns,
		MaxConns:          cfg.DBMaxConns,
		MaxConnIdleTime:   cfg.DBMaxConnIdleTime,
		HealthCheckPeriod: cfg.DBHealthCheckPeriod,
		ConnectTimeout:    cfg.DBConnectTimeout,
		MaxRetries:        cfg.DBMaxRetries,
		InitialBackoff:    cfg.DBInitialBackoff,
	}
	pool, err := database.ConnectWithConfig(dbCtx, cfg.DatabaseURL, poolCfg)
	if err != nil {
		slog.Error("failed to connect to database after retries", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Run migrations with advisory lock (prevents concurrent execution)
	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer migrationCancel()
	if err := database.RunMigrationsWithLock(migrationCtx, pool); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// Start pool stats exporter (updates Prometheus metrics every 10s)
	stopPoolExporter := database.StartPoolStatsExporter(pool)
	defer stopPoolExporter()

	redisClient := setupRedis(context.Background(), cfg)
	defer func() { _ = redisClient.Close() }()

	store := redis.NewSessionRepo(redisClient, clock)
	sentimentStore := redis.NewSentimentStore(redisClient)
	debouncer := redis.NewDebouncer(redisClient)
	voteRateLimiter := redis.NewVoteRateLimiter(redisClient, clock, cfg.VoteRateLimitCapacity, cfg.VoteRateLimitRate)

	// Create config cache with 10-second TTL
	configCache := sentiment.NewConfigCache(10*time.Second, clock)
	stopEviction := configCache.StartEvictionTimer(1 * time.Minute)
	defer stopEviction()

	// Start instance registry (heartbeat every 30s)
	instanceID := fmt.Sprintf("%s-%d", func() string {
		hostname, err := os.Hostname()
		if err != nil {
			return "unknown"
		}
		return hostname
	}(), os.Getpid())
	registry := coordination.NewInstanceRegistry(redisClient, instanceID, 30*time.Second, buildInfo.Commit)
	registryCtx, cancelRegistry := context.WithCancel(context.Background())
	defer cancelRegistry()
	go func() {
		registry.Start(registryCtx)
	}()
	slog.Info("Instance registry started", "instance_id", instanceID)

	// Update registry size metric periodically
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				active, err := registry.GetActiveInstances(registryCtx)
				if err == nil {
					metrics.InstanceRegistrySize.Set(float64(len(active)))
				}
			case <-registryCtx.Done():
				return
			}
		}
	}()

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

	configCacheRepo := redis.NewConfigCacheRepo(redisClient, configRepo)
	engine := sentiment.NewEngine(configCacheRepo, sentimentStore, debouncer, voteRateLimiter, clock, configCache)

	// Start config invalidation subscriber
	// On pub/sub message: evict local in-memory cache + DEL Redis config cache key
	configInvalidator := coordination.NewConfigInvalidator(redisClient, func(broadcasterID string) {
		configCache.Invalidate(broadcasterID)
		if err := configCacheRepo.InvalidateCache(context.Background(), broadcasterID); err != nil {
			slog.Warn("Failed to invalidate Redis config cache via pub/sub",
				"broadcaster_id", broadcasterID, "error", err)
		}
	})
	pubsubCtx, cancelPubsub := context.WithCancel(context.Background())
	defer cancelPubsub()
	go func() {
		configInvalidator.Start(pubsubCtx)
	}()
	slog.Info("Config invalidation subscriber started")

	// Set up webhooks if configured (all three env vars required together)
	// Lazy reference: broadcaster is created after webhooks, but the closure
	// won't be called until the server starts receiving EventSub notifications.
	var broadcasterRef *broadcast.Broadcaster
	hasViewers := func(broadcasterID string) bool {
		if broadcasterRef == nil {
			return true // fail-open during startup
		}
		return broadcasterRef.HasClients(broadcasterID)
	}

	var twitchSvc domain.TwitchService
	var eventsubMgr *twitch.EventSubManager
	var webhookHdlr *twitch.WebhookHandler
	if cfg.WebhookCallbackURL != "" {
		wh, err := initWebhooks(cfg, engine, eventSubRepo, hasViewers)
		if err != nil {
			// EventSub setup failed - continue without webhooks (graceful degradation)
			slog.Warn("EventSub setup failed, continuing without webhooks",
				"error", err,
				"impact", "votes will not be processed until EventSub recovers")
			metrics.EventSubSetupFailuresTotal.Inc()
		} else {
			eventsubMgr = wh.eventsubManager
			webhookHdlr = wh.webhookHandler
			twitchSvc = eventsubMgr
			slog.Info("EventSub configured", "callback_url", cfg.WebhookCallbackURL)
		}
	} else {
		slog.Info("EventSub disabled (WEBHOOK_CALLBACK_URL not set)")
	}

	appSvc := app.NewService(userRepo, configRepo, store, engine, twitchSvc, configCacheRepo, clock, redisClient)

	broadcaster := broadcast.NewBroadcaster(engine, redisClient, clock, 50, 5*time.Second)
	broadcasterRef = broadcaster // resolve lazy reference for webhook hasViewers check

	// Create and start the HTTP server (pass nil explicitly to avoid typed-nil interface)
	var (
		srv    *server.Server
		srvErr error
	)
	if webhookHdlr != nil {
		srv, srvErr = server.NewServer(cfg, appSvc, broadcaster, webhookHdlr, twitchSvc, pool, redisClient)
	} else {
		srv, srvErr = server.NewServer(cfg, appSvc, broadcaster, nil, nil, pool, redisClient)
	}
	if srvErr != nil {
		slog.Error("Failed to create server", "error", srvErr)
		os.Exit(1)
	}

	done := runGracefulShutdown(srv, broadcaster, eventsubMgr)

	slog.Info("Server starting", "port", cfg.Port)
	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}

	<-done
}
