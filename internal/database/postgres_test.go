package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestEncryptDecrypt_WithKey(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, testEncryptionKey)
	require.NoError(t, err)
	defer db.Close()

	plaintext := "my-secret-token-12345"

	// Encrypt
	ciphertext, err := db.encryptToken(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)
	assert.Greater(t, len(ciphertext), len(plaintext))

	// Decrypt
	decrypted, err := db.decryptToken(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptDecrypt_WithoutKey(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, "")
	require.NoError(t, err)
	defer db.Close()

	plaintext := "my-secret-token-12345"

	// Without encryption key, should return plaintext
	ciphertext, err := db.encryptToken(plaintext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, ciphertext)

	decrypted, err := db.decryptToken(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptDecrypt_DecryptInvalidCiphertext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, testEncryptionKey)
	require.NoError(t, err)
	defer db.Close()

	// Try to decrypt invalid hex
	_, err = db.decryptToken("not-valid-hex!!!")
	assert.Error(t, err)

	// Try to decrypt hex that's too short (< nonce size)
	_, err = db.decryptToken("abcd")
	assert.Error(t, err)
}

func TestUpsertUser_Insert(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := db.UpsertUser(ctx, "12345", "testuser", "access_token", "refresh_token", expiry)

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, user.ID)
	assert.NotEqual(t, uuid.Nil, user.OverlayUUID)
	assert.Equal(t, "12345", user.TwitchUserID)
	assert.Equal(t, "testuser", user.TwitchUsername)
	// Compare times in UTC to avoid timezone issues
	assert.WithinDuration(t, expiry, user.TokenExpiry, time.Second)

	// Verify default config was created
	config, err := db.GetConfig(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, config.UserID)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.LeftLabel)
	assert.Equal(t, "For", config.RightLabel)
	assert.InDelta(t, 0.5, config.DecaySpeed, 0.01)
}

func TestUpsertUser_Update(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert
	expiry1 := time.Now().UTC().Add(1 * time.Hour)
	user1, err := db.UpsertUser(ctx, "12345", "testuser", "access1", "refresh1", expiry1)
	require.NoError(t, err)

	originalID := user1.ID
	originalOverlayUUID := user1.OverlayUUID

	// Update with same TwitchUserID
	expiry2 := time.Now().UTC().Add(2 * time.Hour)
	user2, err := db.UpsertUser(ctx, "12345", "testuser_renamed", "access2", "refresh2", expiry2)
	require.NoError(t, err)

	// Should have same IDs but updated fields
	assert.Equal(t, originalID, user2.ID)
	assert.Equal(t, originalOverlayUUID, user2.OverlayUUID)
	assert.Equal(t, "testuser_renamed", user2.TwitchUsername)
	assert.WithinDuration(t, expiry2, user2.TokenExpiry, time.Second)

	// Config should still exist (not duplicated)
	config, err := db.GetConfig(ctx, user2.ID)
	require.NoError(t, err)
	assert.Equal(t, user2.ID, config.UserID)
}

func TestUpsertUser_TokenEncryption(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, testEncryptionKey)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	// Truncate tables
	_, err = db.ExecContext(ctx, "TRUNCATE users, configs CASCADE")
	require.NoError(t, err)

	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := db.UpsertUser(ctx, "12345", "testuser", "plaintext_access", "plaintext_refresh", expiry)
	require.NoError(t, err)

	// Query raw tokens from database
	var rawAccess, rawRefresh string
	err = db.QueryRowContext(ctx, "SELECT access_token, refresh_token FROM users WHERE id = $1", user.ID).Scan(&rawAccess, &rawRefresh)
	require.NoError(t, err)

	// Tokens should be encrypted (not equal to plaintext)
	assert.NotEqual(t, "plaintext_access", rawAccess)
	assert.NotEqual(t, "plaintext_refresh", rawRefresh)

	// User object should have decrypted tokens
	assert.Equal(t, "plaintext_access", user.AccessToken)
	assert.Equal(t, "plaintext_refresh", user.RefreshToken)
}

func TestGetUserByID_Success(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	insertedUser, err := db.UpsertUser(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Get user by ID
	user, err := db.GetUserByID(ctx, insertedUser.ID)
	require.NoError(t, err)
	assert.Equal(t, insertedUser.ID, user.ID)
	assert.Equal(t, insertedUser.TwitchUserID, user.TwitchUserID)
	assert.Equal(t, insertedUser.TwitchUsername, user.TwitchUsername)
	assert.Equal(t, "access", user.AccessToken)
	assert.Equal(t, "refresh", user.RefreshToken)
	assert.WithinDuration(t, expiry, user.TokenExpiry, time.Second)
}

func TestGetUserByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	randomID := uuid.New()
	user, err := db.GetUserByID(ctx, randomID)

	assert.Error(t, err)
	assert.Equal(t, sql.ErrNoRows, err)
	assert.Nil(t, user)
}

func TestGetUserByID_TokenDecryption(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	db, err := Connect(testDatabaseURL, testEncryptionKey)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	// Truncate tables
	_, err = db.ExecContext(ctx, "TRUNCATE users, configs CASCADE")
	require.NoError(t, err)

	expiry := time.Now().UTC().Add(1 * time.Hour)
	insertedUser, err := db.UpsertUser(ctx, "12345", "testuser", "plain_access", "plain_refresh", expiry)
	require.NoError(t, err)

	// Get user - tokens should be decrypted
	user, err := db.GetUserByID(ctx, insertedUser.ID)
	require.NoError(t, err)
	assert.Equal(t, "plain_access", user.AccessToken)
	assert.Equal(t, "plain_refresh", user.RefreshToken)
}

func TestGetUserByOverlayUUID_Success(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	insertedUser, err := db.UpsertUser(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Get user by overlay UUID
	user, err := db.GetUserByOverlayUUID(ctx, insertedUser.OverlayUUID)
	require.NoError(t, err)
	assert.Equal(t, insertedUser.ID, user.ID)
	assert.Equal(t, insertedUser.OverlayUUID, user.OverlayUUID)
	assert.Equal(t, insertedUser.TwitchUserID, user.TwitchUserID)
}

func TestGetUserByOverlayUUID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	randomUUID := uuid.New()
	user, err := db.GetUserByOverlayUUID(ctx, randomUUID)

	assert.Error(t, err)
	assert.Equal(t, sql.ErrNoRows, err)
	assert.Nil(t, user)
}

func TestUpdateUserTokens(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert user
	expiry1 := time.Now().UTC().Add(1 * time.Hour)
	user, err := db.UpsertUser(ctx, "12345", "testuser", "old_access", "old_refresh", expiry1)
	require.NoError(t, err)

	// Update tokens
	expiry2 := time.Now().UTC().Add(2 * time.Hour)
	err = db.UpdateUserTokens(ctx, user.ID, "new_access", "new_refresh", expiry2)
	require.NoError(t, err)

	// Verify update
	updatedUser, err := db.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new_access", updatedUser.AccessToken)
	assert.Equal(t, "new_refresh", updatedUser.RefreshToken)
	assert.WithinDuration(t, expiry2, updatedUser.TokenExpiry, time.Second)
}

func TestGetConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert user (creates default config)
	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := db.UpsertUser(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Get config
	config, err := db.GetConfig(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, config.UserID)
	assert.Equal(t, "yes", config.ForTrigger)
	assert.Equal(t, "no", config.AgainstTrigger)
	assert.Equal(t, "Against", config.LeftLabel)
	assert.Equal(t, "For", config.RightLabel)
	assert.InDelta(t, 0.5, config.DecaySpeed, 0.01)
}

func TestGetConfig_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	randomID := uuid.New()
	config, err := db.GetConfig(ctx, randomID)

	assert.Error(t, err)
	assert.Equal(t, sql.ErrNoRows, err)
	assert.Nil(t, config)
}

func TestUpdateConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := db.UpsertUser(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	// Update config
	err = db.UpdateConfig(ctx, user.ID, "LUL", "BibleThump", "Happy", "Sad", 1.5)
	require.NoError(t, err)

	// Verify update
	config, err := db.GetConfig(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "LUL", config.ForTrigger)
	assert.Equal(t, "BibleThump", config.AgainstTrigger)
	assert.Equal(t, "Happy", config.LeftLabel)
	assert.Equal(t, "Sad", config.RightLabel)
	assert.InDelta(t, 1.5, config.DecaySpeed, 0.01)
}

func TestRotateOverlayUUID(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Insert user
	expiry := time.Now().UTC().Add(1 * time.Hour)
	user, err := db.UpsertUser(ctx, "12345", "testuser", "access", "refresh", expiry)
	require.NoError(t, err)

	oldOverlayUUID := user.OverlayUUID

	// Rotate UUID
	newOverlayUUID, err := db.RotateOverlayUUID(ctx, user.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldOverlayUUID, newOverlayUUID)
	assert.NotEqual(t, uuid.Nil, newOverlayUUID)

	// Verify old UUID no longer works
	_, err = db.GetUserByOverlayUUID(ctx, oldOverlayUUID)
	assert.Error(t, err)
	assert.Equal(t, sql.ErrNoRows, err)

	// Verify new UUID works
	userByNewUUID, err := db.GetUserByOverlayUUID(ctx, newOverlayUUID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, userByNewUUID.ID)

	// Verify GetUserByID also returns new UUID
	userByID, err := db.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, newOverlayUUID, userByID.OverlayUUID)
}
