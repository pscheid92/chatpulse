package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/pscheid92/chatpulse/internal/redis"
	"github.com/prometheus/client_golang/prometheus/testutil"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

var (
	testRedisURL string
	redContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	// Parse flags to check for -short
	flag.Parse()

	// Skip container setup if running in short mode
	if testing.Short() {
		os.Exit(m.Run())
	}

	ctx := context.Background()
	var err error
	redContainer, err = rediscontainer.Run(ctx, "redis:7-alpine")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start redis container: %v\n", err)
		os.Exit(1)
	}

	endpoint, err := redContainer.Endpoint(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get redis endpoint: %v\n", err)
		os.Exit(1)
	}
	testRedisURL = "redis://" + endpoint

	defer func() {
		if err := redContainer.Terminate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate redis container: %v\n", err)
		}
	}()
	os.Exit(m.Run())
}

// TestCleanupOrphans_Integration_RefCountPrevention verifies ref count prevents deletion with real Redis
func TestCleanupOrphans_Integration_RefCountPrevention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	clock := clockwork.NewFakeClock()
	redisClient := setupTestRedisClient(t)

	sessionRepo := redis.NewSessionRepo(redisClient, clock)
	overlayUUID := uuid.New()
	broadcasterID := "test-broadcaster-123"

	// Create a session
	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	err := sessionRepo.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	// Simulate disconnect (set last_disconnect timestamp)
	err = sessionRepo.MarkDisconnected(ctx, overlayUUID)
	require.NoError(t, err)

	// Advance clock to make session "old" (>30s)
	clock.Advance(35 * time.Second)

	// Simulate reconnect: IncrRefCount (session now has ref_count=1)
	refCount, err := sessionRepo.IncrRefCount(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), refCount)

	// Create service with real Redis
	users := &mockUserRepo{}
	configs := &mockConfigRepo{}
	svc := &Service{
		users:         users,
		configs:       configs,
		store:         sessionRepo,
		engine:        &mockEngine{},
		twitch:        nil,
		clock:         clock,
		cleanupStopCh: make(chan struct{}),
	}
	defer svc.Stop()

	initialSkipped := testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active"))
	initialDeleted := testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal)

	// Act: Cleanup should find the orphan but skip it (ref_count > 0)
	svc.CleanupOrphans(ctx)

	// Assert: Session NOT deleted
	exists, err := sessionRepo.SessionExists(ctx, overlayUUID)
	require.NoError(t, err)
	assert.True(t, exists, "Session should still exist (protected by ref count)")

	// Metrics: Skipped count incremented
	assert.Equal(t, initialSkipped+1, testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active")))
	assert.Equal(t, initialDeleted, testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal),
		"No sessions should be deleted")
}

// TestCleanupOrphans_Integration_SuccessfulDeletion verifies cleanup deletes when ref_count=0
func TestCleanupOrphans_Integration_SuccessfulDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	clock := clockwork.NewFakeClock()
	redisClient := setupTestRedisClient(t)

	sessionRepo := redis.NewSessionRepo(redisClient, clock)
	overlayUUID := uuid.New()
	broadcasterID := "test-broadcaster-456"
	userID := uuid.New()

	// Create a session
	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	err := sessionRepo.ActivateSession(ctx, overlayUUID, broadcasterID, config)
	require.NoError(t, err)

	// Increment ref count (simulate connection)
	_, err = sessionRepo.IncrRefCount(ctx, overlayUUID)
	require.NoError(t, err)

	// Disconnect: decrement ref count back to 0
	refCount, err := sessionRepo.DecrRefCount(ctx, overlayUUID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), refCount)

	// Mark disconnected
	err = sessionRepo.MarkDisconnected(ctx, overlayUUID)
	require.NoError(t, err)

	// Advance clock to make session "old" (>30s)
	clock.Advance(35 * time.Second)

	// Setup mocks
	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, uuid uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: uuid}, nil
		},
	}

	var unsubscribeCalled bool
	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, uid uuid.UUID) error {
			unsubscribeCalled = true
			assert.Equal(t, userID, uid)
			return nil
		},
	}

	svc := &Service{
		users:         users,
		configs:       nil,
		store:         sessionRepo,
		engine:        &mockEngine{},
		twitch:        twitch,
		clock:         clock,
		cleanupStopCh: make(chan struct{}),
	}
	defer svc.Stop()

	initialDeleted := testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal)

	// Act: Cleanup should delete the orphan (ref_count=0)
	svc.CleanupOrphans(ctx)

	// Give background goroutine time to complete
	time.Sleep(100 * time.Millisecond)

	// Assert: Session deleted
	exists, err := sessionRepo.SessionExists(ctx, overlayUUID)
	require.NoError(t, err)
	assert.False(t, exists, "Session should be deleted")

	// Metrics: Deleted count incremented
	assert.Equal(t, initialDeleted+1, testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal))

	// Twitch unsubscribe called
	assert.True(t, unsubscribeCalled, "Twitch unsubscribe should be called")
}

// TestCleanupOrphans_Integration_MultipleOrphans verifies batch cleanup with mixed scenarios
func TestCleanupOrphans_Integration_MultipleOrphans(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	clock := clockwork.NewFakeClock()
	redisClient := setupTestRedisClient(t)

	sessionRepo := redis.NewSessionRepo(redisClient, clock)

	// Create 3 orphan sessions:
	// 1. ref_count=0 (should be deleted)
	// 2. ref_count=1 (should be skipped - active)
	// 3. ref_count=0 (should be deleted)

	orphan1 := uuid.New()
	orphan2 := uuid.New()
	orphan3 := uuid.New()

	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		DecaySpeed:     1.0,
	}

	// Setup orphan1 (ref_count=0)
	err := sessionRepo.ActivateSession(ctx, orphan1, "broadcaster-1", config)
	require.NoError(t, err)
	err = sessionRepo.MarkDisconnected(ctx, orphan1)
	require.NoError(t, err)

	// Setup orphan2 (ref_count=1 - active)
	err = sessionRepo.ActivateSession(ctx, orphan2, "broadcaster-2", config)
	require.NoError(t, err)
	_, err = sessionRepo.IncrRefCount(ctx, orphan2)
	require.NoError(t, err)
	err = sessionRepo.MarkDisconnected(ctx, orphan2)
	require.NoError(t, err)

	// Setup orphan3 (ref_count=0)
	err = sessionRepo.ActivateSession(ctx, orphan3, "broadcaster-3", config)
	require.NoError(t, err)
	err = sessionRepo.MarkDisconnected(ctx, orphan3)
	require.NoError(t, err)

	// Advance clock
	clock.Advance(35 * time.Second)

	// Setup mocks
	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, uuid uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: uuid, OverlayUUID: uuid}, nil
		},
	}

	deletedSessions := make(map[uuid.UUID]bool)
	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, uid uuid.UUID) error {
			deletedSessions[uid] = true
			return nil
		},
	}

	svc := &Service{
		users:         users,
		configs:       nil,
		store:         sessionRepo,
		engine:        &mockEngine{},
		twitch:        twitch,
		clock:         clock,
		cleanupStopCh: make(chan struct{}),
	}
	defer svc.Stop()

	initialDeleted := testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal)
	initialSkipped := testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active"))

	// Act
	svc.CleanupOrphans(ctx)
	time.Sleep(100 * time.Millisecond)

	// Assert: orphan1 and orphan3 deleted, orphan2 skipped
	exists1, _ := sessionRepo.SessionExists(ctx, orphan1)
	exists2, _ := sessionRepo.SessionExists(ctx, orphan2)
	exists3, _ := sessionRepo.SessionExists(ctx, orphan3)

	assert.False(t, exists1, "orphan1 should be deleted")
	assert.True(t, exists2, "orphan2 should be skipped (active)")
	assert.False(t, exists3, "orphan3 should be deleted")

	// Metrics
	assert.Equal(t, initialDeleted+2, testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal))
	assert.Equal(t, initialSkipped+1, testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active")))

	// Unsubscribe called for deleted sessions only
	assert.Contains(t, deletedSessions, orphan1)
	assert.NotContains(t, deletedSessions, orphan2)
	assert.Contains(t, deletedSessions, orphan3)
}

// TestCleanupOrphans_Integration_NoOrphans verifies cleanup handles empty list
func TestCleanupOrphans_Integration_NoOrphans(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	clock := clockwork.NewFakeClock()
	redisClient := setupTestRedisClient(t)

	sessionRepo := redis.NewSessionRepo(redisClient, clock)
	svc := &Service{
		users:         nil,
		configs:       nil,
		store:         sessionRepo,
		engine:        &mockEngine{},
		twitch:        nil,
		clock:         clock,
		cleanupStopCh: make(chan struct{}),
	}
	defer svc.Stop()

	initialScans := testutil.ToFloat64(metrics.OrphanCleanupScansTotal)

	// Act: No orphans to clean
	svc.CleanupOrphans(ctx)

	// Assert: Scan metric incremented, no deletions
	assert.Equal(t, initialScans+1, testutil.ToFloat64(metrics.OrphanCleanupScansTotal))
}

// Helper to setup Redis client for integration tests
func setupTestRedisClient(t *testing.T) *goredis.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	client, err := redis.NewClient(ctx, testRedisURL)
	require.NoError(t, err)

	// Flush all keys before each test
	if err := client.FlushAll(ctx).Err(); err != nil {
		t.Fatalf("failed to flush redis: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}
