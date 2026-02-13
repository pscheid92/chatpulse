package coordination

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
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

// mockEngine is a simple mock for domain.Engine that tracks invalidations.
type mockEngine struct {
	invalidations []uuid.UUID
}

func (m *mockEngine) GetCurrentValue(ctx context.Context, sessionUUID uuid.UUID) (float64, error) {
	return 0.0, nil
}

func (m *mockEngine) ProcessVote(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (float64, bool) {
	return 0.0, true
}

func (m *mockEngine) ResetSentiment(ctx context.Context, sessionUUID uuid.UUID) error {
	return nil
}

func (m *mockEngine) InvalidateConfigCache(overlayUUID uuid.UUID) {
	m.invalidations = append(m.invalidations, overlayUUID)
}

func TestInstanceRegistry_RegisterAndGetActive(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	registry := NewInstanceRegistry(redisClient, "test-instance-1", 1*time.Second, "v1.0.0")

	// Register once
	registry.register(ctx)

	// Verify instance is active
	active, err := registry.GetActiveInstances(ctx)
	require.NoError(t, err)
	assert.Contains(t, active, "test-instance-1")

	// Verify instance info
	infos, err := registry.GetInstanceInfo(ctx)
	require.NoError(t, err)
	assert.Len(t, infos, 1)
	assert.Equal(t, "test-instance-1", infos[0].InstanceID)
	assert.Equal(t, "v1.0.0", infos[0].Version)
}

func TestInstanceRegistry_HeartbeatExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	registry := NewInstanceRegistry(redisClient, "test-instance-2", 1*time.Second, "v1.0.0")

	// Register with old timestamp (simulating expired heartbeat)
	key := instancesKey
	value := InstanceInfo{
		InstanceID: "test-instance-2",
		Timestamp:  time.Now().Unix() - 70, // 70 seconds ago
		Version:    "v1.0.0",
	}
	data, _ := json.Marshal(value)
	redisClient.HSet(ctx, key, "test-instance-2", data)

	// Should not appear in active instances
	active, err := registry.GetActiveInstances(ctx)
	require.NoError(t, err)
	assert.NotContains(t, active, "test-instance-2")
}

func TestInstanceRegistry_MultipleInstances(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	registry1 := NewInstanceRegistry(redisClient, "instance-1", 1*time.Second, "v1.0.0")
	registry2 := NewInstanceRegistry(redisClient, "instance-2", 1*time.Second, "v1.0.0")
	registry3 := NewInstanceRegistry(redisClient, "instance-3", 1*time.Second, "v1.1.0")

	// Register all three
	registry1.register(ctx)
	registry2.register(ctx)
	registry3.register(ctx)

	// All should be active
	active, err := registry1.GetActiveInstances(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 3)
	assert.Contains(t, active, "instance-1")
	assert.Contains(t, active, "instance-2")
	assert.Contains(t, active, "instance-3")
}

func TestInstanceRegistry_Unregister(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	registry := NewInstanceRegistry(redisClient, "test-instance-4", 1*time.Second, "v1.0.0")

	// Register
	registry.register(ctx)

	// Verify active
	active, err := registry.GetActiveInstances(ctx)
	require.NoError(t, err)
	assert.Contains(t, active, "test-instance-4")

	// Unregister
	registry.unregister()

	// Should no longer be active
	active, err = registry.GetActiveInstances(ctx)
	require.NoError(t, err)
	assert.NotContains(t, active, "test-instance-4")
}

func TestConfigInvalidator_HandleInvalidation(t *testing.T) {
	engine := &mockEngine{invalidations: []uuid.UUID{}}
	redisClient := redis.NewClient(&redis.Options{})
	invalidator := NewConfigInvalidator(redisClient, engine)

	overlayUUID := uuid.New()

	// Process valid UUID
	invalidator.handleInvalidation(overlayUUID.String())

	// Verify engine was called
	assert.Len(t, engine.invalidations, 1)
	assert.Equal(t, overlayUUID, engine.invalidations[0])
}

func TestConfigInvalidator_InvalidPayload(t *testing.T) {
	engine := &mockEngine{invalidations: []uuid.UUID{}}
	redisClient := redis.NewClient(&redis.Options{})
	invalidator := NewConfigInvalidator(redisClient, engine)

	// Process invalid UUID
	invalidator.handleInvalidation("not-a-uuid")

	// Engine should not be called
	assert.Len(t, engine.invalidations, 0)
}

func TestPublishConfigInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	overlayUUID := uuid.New()

	// Publish should succeed
	err := PublishConfigInvalidation(ctx, redisClient, overlayUUID)
	assert.NoError(t, err)
}

func TestLeaderElection_TryBecomeLeader(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	leader := NewLeaderElection(redisClient, "instance-1", "leader:test", 10*time.Second)

	// First instance becomes leader
	success, err := leader.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	// Verify is leader
	isLeader, err := leader.IsLeader(ctx)
	require.NoError(t, err)
	assert.True(t, isLeader)
}

func TestLeaderElection_MultipleInstances(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	leader1 := NewLeaderElection(redisClient, "instance-1", "leader:test2", 10*time.Second)
	leader2 := NewLeaderElection(redisClient, "instance-2", "leader:test2", 10*time.Second)

	// First instance becomes leader
	success1, err := leader1.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success1)

	// Second instance fails to become leader
	success2, err := leader2.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.False(t, success2)

	// Verify leader states
	isLeader1, err := leader1.IsLeader(ctx)
	require.NoError(t, err)
	assert.True(t, isLeader1)

	isLeader2, err := leader2.IsLeader(ctx)
	require.NoError(t, err)
	assert.False(t, isLeader2)
}

func TestLeaderElection_RenewLease(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	leader := NewLeaderElection(redisClient, "instance-1", "leader:test3", 10*time.Second)

	// Become leader
	success, err := leader.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	// Renew lease should succeed
	err = leader.RenewLease(ctx)
	assert.NoError(t, err)

	// Still leader
	isLeader, err := leader.IsLeader(ctx)
	require.NoError(t, err)
	assert.True(t, isLeader)
}

func TestLeaderElection_RenewLeaseFailsWhenNotLeader(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	leader1 := NewLeaderElection(redisClient, "instance-1", "leader:test4", 10*time.Second)
	leader2 := NewLeaderElection(redisClient, "instance-2", "leader:test4", 10*time.Second)

	// Instance 1 becomes leader
	success, err := leader1.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	// Instance 2 tries to renew (should fail)
	err = leader2.RenewLease(ctx)
	assert.ErrorIs(t, err, ErrNotLeader)
}

func TestLeaderElection_ReleaseLease(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	leader := NewLeaderElection(redisClient, "instance-1", "leader:test5", 10*time.Second)

	// Become leader
	success, err := leader.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	// Release lease
	err = leader.ReleaseLease(ctx)
	require.NoError(t, err)

	// No longer leader
	isLeader, err := leader.IsLeader(ctx)
	require.NoError(t, err)
	assert.False(t, isLeader)

	// Another instance can now become leader
	leader2 := NewLeaderElection(redisClient, "instance-2", "leader:test5", 10*time.Second)
	success, err = leader2.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)
}

func TestLeaderElection_TTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	leader1 := NewLeaderElection(redisClient, "instance-1", "leader:test6", 1*time.Second)
	leader2 := NewLeaderElection(redisClient, "instance-2", "leader:test6", 1*time.Second)

	// Instance 1 becomes leader
	success, err := leader1.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	// Wait for TTL to expire
	time.Sleep(2 * time.Second)

	// Instance 2 can now become leader
	success, err = leader2.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	isLeader, err := leader2.IsLeader(ctx)
	require.NoError(t, err)
	assert.True(t, isLeader)
}

// setupTestRedis creates a Redis client for testing.
// Tests using this must check testing.Short() and skip if true.
func setupTestRedis(t *testing.T) *redis.Client {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test")
	}

	opts, err := redis.ParseURL(testRedisURL)
	require.NoError(t, err)

	client := redis.NewClient(opts)

	// Flush all keys before each test
	ctx := context.Background()
	err = client.FlushAll(ctx).Err()
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}
