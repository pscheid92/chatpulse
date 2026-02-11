package database

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
)

//go:embed sqlc/schemas/*.sql
var migrationFiles embed.FS

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

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
		log.Printf("Notice: could not get current version (likely fresh DB): %v", err)
	} else {
		log.Printf("Current DB version: %d", currentVersion)
	}

	if err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}
