package database

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

//go:embed sqlc/schemas/*.sql
var migrationFiles embed.FS

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Add metrics tracer to collect query metrics
	poolCfg.ConnConfig.Tracer = &MetricsTracer{}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

// UpdatePoolMetrics updates database connection pool metrics
// This should be called periodically (e.g., every 30 seconds) to track pool health
func UpdatePoolMetrics(pool *pgxpool.Pool) {
	stats := pool.Stat()

	// Track active connections (connections currently in use)
	metrics.DBConnectionsCurrent.WithLabelValues("active").Set(float64(stats.AcquiredConns()))

	// Track idle connections (connections in pool but not in use)
	metrics.DBConnectionsCurrent.WithLabelValues("idle").Set(float64(stats.IdleConns()))
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
