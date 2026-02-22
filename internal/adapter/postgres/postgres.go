package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

	slog.Info("Database SSL mode", "sslmode", extractSSLMode(databaseURL))

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	slog.Info("Database connected", "min_conns", poolCfg.MinConns, "max_conns", poolCfg.MaxConns)
	return pool, nil
}

func extractSSLMode(databaseURL string) string {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "unknown"
	}
	mode := strings.ToLower(u.Query().Get("sslmode"))
	if mode == "" {
		return "prefer (default)"
	}
	return mode
}

const (
	// migrationLockID is a PostgreSQL advisory lock ID for coordinating migrations.
	// Value: 0x636861747075 ("chatpu" in ASCII hex)
	migrationLockID             = 0x636861747075
	migrationLockReleaseTimeout = 5 * time.Second
)

func RunMigrationsWithLock(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection for migration: %w", err)
	}
	defer conn.Release()

	cancel, err := migrationLock(ctx, conn.Conn(), migrationLockReleaseTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	slog.Info("running database migrations")
	return runMigrations(ctx, conn.Conn())
}

func runMigrations(ctx context.Context, conn *pgx.Conn) error {
	migrationFS, err := fs.Sub(migrationFiles, "sqlc/schemas")
	if err != nil {
		return fmt.Errorf("failed to read migrations: %w", err)
	}

	migrator, err := migrate.NewMigrator(ctx, conn, "public.schema_version")
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}

	if err := migrator.LoadMigrations(migrationFS); err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	currentVersion, err := migrator.GetCurrentVersion(ctx)
	if err != nil {
		slog.Debug("could not get current DB version (likely fresh DB)", "error", err)
	} else {
		slog.Info("current DB version", "version", currentVersion)
	}

	if err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	return nil
}

func migrationLock(ctx context.Context, conn *pgx.Conn, releaseTimeout time.Duration) (cancel func(), err error) {
	cancel = func() { /* EMPTY */ }

	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		err = fmt.Errorf("failed to acquire migration lock: %w", err)
		return
	}

	cancel = func() {
		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()

		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			slog.Error("failed to release migration lock", "error", err)
		}
	}
	return
}
