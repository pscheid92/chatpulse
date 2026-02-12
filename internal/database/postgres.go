package database

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

//go:embed sqlc/schemas/*.sql
var migrationFiles embed.FS

// PoolConfig contains connection pool configuration
type PoolConfig struct {
	MinConns          int32
	MaxConns          int32
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	ConnectTimeout    time.Duration
	MaxRetries        int
	InitialBackoff    time.Duration
}

// DefaultPoolConfig returns recommended pool settings
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MinConns:          2,
		MaxConns:          10,
		MaxConnIdleTime:   5 * time.Minute,
		HealthCheckPeriod: 1 * time.Minute,
		ConnectTimeout:    5 * time.Second,
		MaxRetries:        3,
		InitialBackoff:    1 * time.Second,
	}
}

// Connect establishes a connection pool with retry logic and explicit pool configuration.
// Uses exponential backoff (1s, 2s, 4s) for transient connection failures.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return ConnectWithConfig(ctx, databaseURL, DefaultPoolConfig())
}

// ConnectWithConfig establishes a connection pool with custom configuration
func ConnectWithConfig(ctx context.Context, databaseURL string, cfg PoolConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Configure connection pool (explicit settings for predictable behavior)
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// Add metrics tracer to collect query metrics
	poolCfg.ConnConfig.Tracer = &MetricsTracer{}

	// Retry strategy: exponential backoff for transient failures
	var pool *pgxpool.Pool
	backoff := cfg.InitialBackoff

	for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, poolCfg)
		if err == nil {
			// Success: verify with ping
			pingErr := pool.Ping(ctx)
			if pingErr == nil {
				slog.Info("database connected",
					"attempt", attempt,
					"min_conns", cfg.MinConns,
					"max_conns", cfg.MaxConns)
				return pool, nil
			}
			// Ping failed, close and retry
			pool.Close()
			err = pingErr
		}

		if attempt < cfg.MaxRetries {
			slog.Warn("database connection failed, retrying",
				"attempt", attempt,
				"backoff_seconds", backoff.Seconds(),
				"error", err)

			select {
			case <-time.After(backoff):
				backoff *= 2 // Exponential backoff
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		}
	}

	return nil, fmt.Errorf("database connection failed after %d attempts: %w", cfg.MaxRetries, err)
}

// UpdatePoolMetrics updates database connection pool metrics
// This should be called periodically (e.g., every 30 seconds) to track pool health
func UpdatePoolMetrics(pool *pgxpool.Pool) {
	stats := pool.Stat()

	// Track active connections (connections currently in use)
	metrics.DBConnectionsCurrent.WithLabelValues("active").Set(float64(stats.AcquiredConns()))

	// Track idle connections (connections in pool but not in use)
	metrics.DBConnectionsCurrent.WithLabelValues("idle").Set(float64(stats.IdleConns()))

	// Track total connections in pool
	metrics.DBConnectionsCurrent.WithLabelValues("total").Set(float64(stats.TotalConns()))

	// Track maximum connections configured
	metrics.DBConnectionsCurrent.WithLabelValues("max").Set(float64(stats.MaxConns()))
}

// StartPoolStatsExporter exports pool statistics to Prometheus metrics every 10 seconds.
// Returns a cleanup function to stop the exporter.
func StartPoolStatsExporter(pool *pgxpool.Pool) func() {
	ticker := time.NewTicker(10 * time.Second)
	done := make(chan struct{})

	go func() {
		// Update immediately on start
		UpdatePoolMetrics(pool)

		for {
			select {
			case <-ticker.C:
				UpdatePoolMetrics(pool)
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	return func() {
		close(done)
	}
}

// migrationLockID is a PostgreSQL advisory lock ID for coordinating migrations.
// Value: 0x636861747075 ("chatpu" in ASCII hex)
const migrationLockID = 0x636861747075

// RunMigrationsWithLock runs database migrations with an advisory lock to prevent
// concurrent migration execution across multiple instances.
func RunMigrationsWithLock(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection for migration: %w", err)
	}
	defer conn.Release()

	// Try to acquire advisory lock (non-blocking check first)
	var lockAcquired bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockID).Scan(&lockAcquired)
	if err != nil {
		return fmt.Errorf("failed to check migration lock: %w", err)
	}

	if !lockAcquired {
		// Another instance is running migrations, wait for it (blocking)
		slog.Info("waiting for migration lock (another instance migrating)")
		err = conn.QueryRow(ctx, "SELECT pg_advisory_lock($1)", migrationLockID).Scan(&lockAcquired)
		if err != nil {
			return fmt.Errorf("failed to acquire migration lock: %w", err)
		}
	}

	// Release lock on function exit (use background context in case ctx is cancelled)
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID).Scan(&unlocked); err != nil {
			slog.Error("failed to release migration lock", "error", err)
		}
	}()

	slog.Info("running database migrations")
	return RunMigrations(ctx, pool)
}

func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection from pool: %w", err)
	}
	defer connection.Release()

	migrationFS, err := fs.Sub(migrationFiles, "sqlc/schemas")
	if err != nil {
		return fmt.Errorf("failed to read migrations: %w", err)
	}

	migrator, err := migrate.NewMigrator(ctx, connection.Conn(), "public.schema_version")
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}

	if err := migrator.LoadMigrations(migrationFS); err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	currentVersion, err := migrator.GetCurrentVersion(ctx)
	if err != nil {
		slog.Debug("Could not get current DB version (likely fresh DB)", "error", err)
	} else {
		slog.Info("Current DB version", "version", currentVersion)
	}

	if err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}
