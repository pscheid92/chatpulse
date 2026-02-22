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
	"slices"
	"syscall"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/adapter/eventpublisher"
	"github.com/pscheid92/chatpulse/internal/adapter/httpserver"
	"github.com/pscheid92/chatpulse/internal/adapter/metrics"
	"github.com/pscheid92/chatpulse/internal/adapter/postgres"
	"github.com/pscheid92/chatpulse/internal/adapter/redis"
	"github.com/pscheid92/chatpulse/internal/adapter/twitch"
	"github.com/pscheid92/chatpulse/internal/adapter/websocket"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/config"
	"github.com/pscheid92/chatpulse/internal/platform/crypto"
	"github.com/pscheid92/chatpulse/internal/platform/logging"
	"github.com/pscheid92/chatpulse/internal/platform/version"
	goredis "github.com/redis/go-redis/v9"
)

const (
	dbConnectTimeout     = 30 * time.Second
	migrationTimeout     = 60 * time.Second
	eventsubSetupTimeout = 30 * time.Second
	configCacheTTL       = 10 * time.Second
	configEvictInterval  = 1 * time.Minute
)

type database struct {
	pool         *pgxpool.Pool
	streamerRepo *postgres.StreamerRepo
	configRepo   *postgres.ConfigRepo
	eventSubRepo *postgres.EventSubRepo
	healthChecks []httpserver.HealthCheck
}

func setupDatabase(cfg *config.Config) (database, error) {
	dbCtx, dbCancel := context.WithTimeout(context.Background(), dbConnectTimeout)
	defer dbCancel()

	pool, err := postgres.Connect(dbCtx, cfg.DatabaseURL)
	if err != nil {
		return database{}, fmt.Errorf("connect to database: %w", err)
	}

	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer migrationCancel()
	if err := postgres.RunMigrationsWithLock(migrationCtx, pool); err != nil {
		pool.Close()
		return database{}, fmt.Errorf("run migrations: %w", err)
	}

	cryptoSvc, err := crypto.NewAesGcmCryptoService(cfg.TokenEncryptionKey)
	if err != nil {
		pool.Close()
		return database{}, fmt.Errorf("create crypto service: %w", err)
	}

	return database{
		pool:         pool,
		streamerRepo: postgres.NewStreamerRepo(pool, cryptoSvc),
		configRepo:   postgres.NewConfigRepo(pool),
		eventSubRepo: postgres.NewEventSubRepo(pool),
		healthChecks: []httpserver.HealthCheck{
			{Name: "postgres", Check: pool.Ping},
		},
	}, nil
}

type redisInfra struct {
	client       *goredis.Client
	sentiment    *redis.SentimentStore
	debouncer    *redis.Debouncer
	configCache  *redis.ConfigCacheRepo
	stopEviction func()
	cancelPubsub context.CancelFunc
	healthChecks []httpserver.HealthCheck
}

func setupRedis(cfg *config.Config, configRepo domain.ConfigRepository) (redisInfra, error) {
	client, err := redis.NewClient(context.Background(), cfg.RedisURL)
	if err != nil {
		return redisInfra{}, fmt.Errorf("connect to Redis: %w", err)
	}

	configCache := redis.NewConfigCacheRepo(client, configRepo, configCacheTTL)
	stopEviction := configCache.StartEvictionTimer(configEvictInterval)

	invalidator := redis.NewConfigInvalidationSubscriber(client, configCache)
	pubsubCtx, cancelPubsub := context.WithCancel(context.Background())
	go invalidator.Start(pubsubCtx)
	slog.Info("Config invalidation subscriber started")

	return redisInfra{
		client:       client,
		sentiment:    redis.NewSentimentStore(client),
		debouncer:    redis.NewDebouncer(client),
		configCache:  configCache,
		stopEviction: stopEviction,
		cancelPubsub: cancelPubsub,
		healthChecks: []httpserver.HealthCheck{
			{Name: "redis", Check: func(ctx context.Context) error {
				return client.Ping(ctx).Err()
			}},
		},
	}, nil
}

type realtimeInfra struct {
	node      *centrifuge.Node
	publisher *websocket.Publisher
	presence  *websocket.PresenceChecker
}

func setupWebSocket(streamerRepo domain.StreamerRepository, redisAddr string, configCache domain.ConfigSource, wsMetrics *metrics.WebSocketMetrics, logLevel string) (realtimeInfra, error) {
	node, err := websocket.NewNode(streamerRepo, wsMetrics, logLevel)
	if err != nil {
		return realtimeInfra{}, fmt.Errorf("create Centrifuge node: %w", err)
	}

	if err := websocket.SetupRedis(node, redisAddr); err != nil {
		return realtimeInfra{}, fmt.Errorf("setup Centrifuge Redis: %w", err)
	}
	if err := node.Run(); err != nil {
		return realtimeInfra{}, fmt.Errorf("start Centrifuge node: %w", err)
	}

	slog.Info("Centrifuge node started")

	return realtimeInfra{
		node:      node,
		publisher: websocket.NewPublisher(node, configCache, wsMetrics),
		presence:  websocket.NewPresenceChecker(node),
	}, nil
}

func initEventSubManager(cfg *config.Config, eventSubRepo *postgres.EventSubRepo) (*twitch.EventSubManager, error) {
	mgr, err := twitch.NewEventSubManager(cfg.TwitchClientID, cfg.TwitchClientSecret, eventSubRepo, cfg.WebhookCallbackURL, cfg.WebhookSecret, cfg.BotUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to create EventSub manager: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), eventsubSetupTimeout)
	defer cancel()

	if err := mgr.Setup(ctx); err != nil {
		return nil, fmt.Errorf("failed to setup webhook conduit: %w", err)
	}

	return mgr, nil
}

func initWebhookHandler(cfg *config.Config, ovl *app.Overlay, presence twitch.ViewerPresence, publisher domain.EventPublisher, onVoteApplied func(string), voteMetrics *metrics.VoteMetrics) *twitch.WebhookHandler {
	return twitch.NewWebhookHandler(cfg.WebhookSecret, ovl, presence, publisher, onVoteApplied, voteMetrics)
}

func runGracefulShutdown(srv *httpserver.Server, node *centrifuge.Node, timeout time.Duration) <-chan struct{} {
	done := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		slog.Info("Shutdown signal received, cleaning up...")

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("Server shutdown error", "error", err)
		}
		if err := node.Shutdown(ctx); err != nil {
			slog.Error("Centrifuge node shutdown error", "error", err)
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

func logStartup(cfg *config.Config) {
	buildInfo := version.Get()
	slog.Info("Application starting", "env", cfg.AppEnv, "port", cfg.Port, "version", buildInfo.Version, "commit", buildInfo.Commit, "build_time", buildInfo.BuildTime)
}

func main() {
	cfg := setupConfig()
	logging.InitLogger(cfg.LogLevel, cfg.LogFormat)
	logStartup(cfg)

	db, err := setupDatabase(cfg)
	if err != nil {
		slog.Error("Database setup failed", "error", err)
		os.Exit(1)
	}
	defer db.pool.Close()

	rc, err := setupRedis(cfg, db.configRepo)
	if err != nil {
		slog.Error("Redis setup failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = rc.client.Close() }()
	defer rc.stopEviction()
	defer rc.cancelPubsub()

	// Metrics setup
	reg := metrics.NewRegistry()
	httpMetrics := metrics.NewHTTPMetrics(reg)
	voteMetrics := metrics.NewVoteMetrics(reg)
	cacheMetrics := metrics.NewCacheMetrics(reg)
	wsMetrics := metrics.NewWebSocketMetrics(reg)
	rc.configCache.SetCacheMetrics(cacheMetrics)

	ovl := app.NewOverlay(rc.configCache, rc.sentiment, rc.debouncer)

	ws, err := setupWebSocket(db.streamerRepo, rc.client.Options().Addr, rc.configCache, wsMetrics, cfg.LogLevel)
	if err != nil {
		slog.Error("WebSocket setup failed", "error", err)
		os.Exit(1)
	}

	publisher := eventpublisher.New(ws.publisher, rc.configCache, rc.client)

	ticker := app.NewSnapshotTicker(rc.sentiment, rc.configCache, publisher)
	tickerCtx, cancelTicker := context.WithCancel(context.Background())
	go ticker.Run(tickerCtx)
	defer cancelTicker()
	slog.Info("Snapshot ticker started")

	eventsubMgr, err := initEventSubManager(cfg, db.eventSubRepo)
	if err != nil {
		slog.Error("EventSub setup failed", "error", err)
		os.Exit(1)
	}
	slog.Info("EventSub configured", "callback_url", cfg.WebhookCallbackURL)

	webhookHdlr := initWebhookHandler(cfg, ovl, ws.presence, publisher, ticker.Track, voteMetrics)
	appSvc := app.NewService(db.streamerRepo, db.configRepo, ovl, eventsubMgr, publisher)

	healthChecks := slices.Concat(db.healthChecks, rc.healthChecks)

	wsHandler := centrifuge.NewWebsocketHandler(ws.node, centrifuge.WebsocketConfig{
		CheckOrigin: websocket.NewCheckOrigin(cfg.WebhookCallbackURL, cfg.AppEnv == "development"),
	})

	srv, err := httpserver.NewServer(cfg, appSvc, ws.presence, wsHandler, http.HandlerFunc(webhookHdlr.HandleEventSub), eventsubMgr, healthChecks, httpMetrics, metrics.Handler(reg))
	if err != nil {
		slog.Error("Failed to create server", "error", err)
		os.Exit(1)
	}

	done := runGracefulShutdown(srv, ws.node, cfg.ShutdownTimeout)

	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}

	<-done
}
