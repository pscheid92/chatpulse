package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	testDB          *DB
	testDatabaseURL string
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Start PostgreSQL container once for all tests
	postgresContainer, err := postgres.Run(ctx,
		"postgres:15-alpine",
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
	testDB, err = Connect(testDatabaseURL, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to test database: %v\n", err)
		os.Exit(1)
	}
	defer testDB.Close()

	// Run migrations
	if err := testDB.RunMigrations(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	os.Exit(code)
}

// setupTestDB returns a DB instance and registers cleanup to truncate tables
func setupTestDB(t *testing.T) *DB {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, err := testDB.ExecContext(ctx, "TRUNCATE users, configs CASCADE")
		if err != nil {
			t.Logf("Failed to truncate tables: %v", err)
		}
	})

	return testDB
}

func TestConnect_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, "")
	require.NoError(t, err)
	require.NotNil(t, db)
	defer db.Close()

	// Verify connection works
	ctx := context.Background()
	err = db.PingContext(ctx)
	require.NoError(t, err)
}

func TestConnect_InvalidURL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect("postgres://invalid:invalid@localhost:9999/nonexistent", "")
	assert.Error(t, err)
	assert.Nil(t, db)
}

func TestConnect_EncryptionKeyValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		name    string
		keyHex  string
		wantErr bool
	}{
		{
			name:    "empty key is valid",
			keyHex:  "",
			wantErr: false,
		},
		{
			name:    "32 bytes (64 hex chars) is valid",
			keyHex:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantErr: false,
		},
		{
			name:    "31 bytes is invalid",
			keyHex:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd",
			wantErr: true,
		},
		{
			name:    "33 bytes is invalid",
			keyHex:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00",
			wantErr: true,
		},
		{
			name:    "invalid hex is invalid",
			keyHex:  "zzzz",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := Connect(testDatabaseURL, tt.keyHex)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, db)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, db)
				if db != nil {
					db.Close()
				}
			}
		})
	}
}

func TestHealthCheck(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	err := db.HealthCheck(ctx)
	assert.NoError(t, err)
}

func TestHealthCheck_ContextCanceled(t *testing.T) {
	db := setupTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := db.HealthCheck(ctx)
	assert.Error(t, err)
}

func TestRunMigrations_Idempotency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, "")
	require.NoError(t, err)
	defer db.Close()

	// Run migrations twice - should not error
	err = db.RunMigrations()
	require.NoError(t, err)

	err = db.RunMigrations()
	require.NoError(t, err)
}

func TestRunMigrations_SchemaVerification(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Verify users table exists with expected columns
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'users'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify configs table exists
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_name = 'configs'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify overlay_uuid column exists in users table
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT FROM information_schema.columns
			WHERE table_name = 'users' AND column_name = 'overlay_uuid'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)
}
