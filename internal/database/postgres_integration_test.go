package database

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	testPool        *pgxpool.Pool
	testDatabaseURL string
)

func TestMain(m *testing.M) {
	// Parse flags to check for -short
	flag.Parse()

	// Skip container setup if running in short mode
	if testing.Short() {
		os.Exit(m.Run())
	}

	ctx := context.Background()

	// Start PostgreSQL container once for all tests
	postgresContainer, err := postgres.Run(ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start postgres container: %v\n", err)
		os.Exit(1)
	}

	defer func() {
		if err := postgresContainer.Terminate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to terminate postgres container: %v\n", err)
		}
	}()

	// Get connection string
	connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get connection string: %v\n", err)
		os.Exit(1)
	}
	testDatabaseURL = connStr

	// Connect to database
	testPool, err = Connect(ctx, testDatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to test database: %v\n", err)
		os.Exit(1)
	}
	defer testPool.Close()

	// Run migrations
	if err := RunMigrations(ctx, testPool); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	os.Exit(code)
}

// setupTestDB returns a pool and registers cleanup to truncate tables
func setupTestDB(t *testing.T) *pgxpool.Pool {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, err := testPool.Exec(ctx, "TRUNCATE users, configs, eventsub_subscriptions CASCADE")
		if err != nil {
			t.Logf("Failed to truncate tables: %v", err)
		}
	})

	return testPool
}

func TestConnect_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	pool, err := Connect(ctx, testDatabaseURL)
	require.NoError(t, err)
	require.NotNil(t, pool)
	defer pool.Close()

	// Verify connection works
	err = pool.Ping(ctx)
	require.NoError(t, err)
}

func TestConnect_InvalidURL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	pool, err := Connect(ctx, "postgres://invalid:invalid@localhost:9999/nonexistent")
	assert.Error(t, err)
	assert.Nil(t, pool)
}

func TestRunMigrations_Idempotency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	pool, err := Connect(ctx, testDatabaseURL)
	require.NoError(t, err)
	defer pool.Close()

	// Run migrations twice - should not error
	err = RunMigrations(ctx, pool)
	require.NoError(t, err)

	err = RunMigrations(ctx, pool)
	require.NoError(t, err)
}

func TestRunMigrations_SchemaVerification(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()

	// Verify users table exists with expected columns
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'users'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify configs table exists
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'configs'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify overlay_uuid column exists in users table
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.columns
			WHERE table_name = 'users' AND column_name = 'overlay_uuid'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)
}
