package coordination

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestThreeInstanceCoordination tests a realistic scenario with 3 instances:
// - All 3 register and maintain heartbeats
// - Config invalidation propagates to all instances
// - Leader election ensures only 1 instance runs cleanup
// - Leader failover works when leader crashes
func TestThreeInstanceCoordination(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	redisClient := setupTestRedis(t)

	// Create 3 instances
	instances := make([]*testInstance, 3)
	for i := 0; i < 3; i++ {
		instances[i] = newTestInstance(t, redisClient, i+1)
	}

	// Start all instances
	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(inst *testInstance) {
			defer wg.Done()
			inst.start(ctx)
		}(inst)
	}

	// Wait for all instances to register
	time.Sleep(100 * time.Millisecond)

	// Verify all 3 instances are active
	active, err := instances[0].registry.GetActiveInstances(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 3, "All 3 instances should be active")

	// Test leader election - one instance should be leader
	leaders := 0
	var leaderInstance *testInstance
	for _, inst := range instances {
		isLeader, err := inst.leader.IsLeader(ctx)
		require.NoError(t, err)
		if isLeader {
			leaders++
			leaderInstance = inst
		}
	}
	assert.Equal(t, 1, leaders, "Exactly one instance should be leader")
	require.NotNil(t, leaderInstance)

	// Test config invalidation propagation
	overlayUUID := uuid.New()
	err = PublishConfigInvalidation(ctx, redisClient, overlayUUID)
	require.NoError(t, err)

	// Wait for pub/sub delivery
	time.Sleep(200 * time.Millisecond)

	// All instances should have received the invalidation
	for i, inst := range instances {
		assert.Contains(t, inst.engine.invalidations, overlayUUID,
			"Instance %d should have received config invalidation", i+1)
	}

	// Test leader failover - release leader's lease
	t.Logf("Leader instance: %s", leaderInstance.id)
	err = leaderInstance.leader.ReleaseLease(ctx)
	require.NoError(t, err)

	// Wait a bit and trigger other instances to try becoming leader
	time.Sleep(100 * time.Millisecond)

	// Have all non-leader instances try to become leader
	for _, inst := range instances {
		if inst != leaderInstance {
			_, _ = inst.leader.TryBecomeLeader(ctx)
		}
	}

	// A different instance should now be leader
	leaders = 0
	var newLeader *testInstance
	for _, inst := range instances {
		isLeader, err := inst.leader.IsLeader(ctx)
		require.NoError(t, err)
		if isLeader {
			leaders++
			newLeader = inst
		}
	}
	assert.Equal(t, 1, leaders, "Exactly one instance should be new leader")
	require.NotNil(t, newLeader, "A new leader should have been elected")
	assert.NotEqual(t, leaderInstance.id, newLeader.id, "New leader should be different")

	// Stop all instances
	cancel()
	wg.Wait()

	// Verify all instances unregistered
	time.Sleep(100 * time.Millisecond)
	active, err = instances[0].registry.GetActiveInstances(context.Background())
	require.NoError(t, err)
	assert.Len(t, active, 0, "All instances should have unregistered")
}

// TestLeaderElectionFailover tests that a new leader is elected when the
// current leader's TTL expires (simulating a crash).
func TestLeaderElectionFailover(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	// Short TTL for faster test
	leader1 := NewLeaderElection(redisClient, "instance-1", "leader:failover", 1*time.Second)
	leader2 := NewLeaderElection(redisClient, "instance-2", "leader:failover", 1*time.Second)

	// Instance 1 becomes leader
	success, err := leader1.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success)

	// Instance 2 cannot become leader
	success, err = leader2.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.False(t, success)

	// Wait for leader1's TTL to expire (simulating crash)
	time.Sleep(2 * time.Second)

	// Instance 2 should now be able to become leader
	success, err = leader2.TryBecomeLeader(ctx)
	require.NoError(t, err)
	assert.True(t, success, "Instance 2 should become leader after failover")

	isLeader, err := leader2.IsLeader(ctx)
	require.NoError(t, err)
	assert.True(t, isLeader)
}

// TestConcurrentLeaderElection tests that multiple instances competing for
// leadership results in exactly one leader.
func TestConcurrentLeaderElection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	redisClient := setupTestRedis(t)

	const numInstances = 10
	leaders := make([]*LeaderElection, numInstances)
	for i := 0; i < numInstances; i++ {
		leaders[i] = NewLeaderElection(
			redisClient,
			string(rune('A'+i)),
			"leader:concurrent",
			10*time.Second,
		)
	}

	// All instances try to become leader concurrently
	var wg sync.WaitGroup
	results := make([]bool, numInstances)
	for i := 0; i < numInstances; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			success, _ := leaders[idx].TryBecomeLeader(ctx)
			results[idx] = success
		}(i)
	}

	wg.Wait()

	// Exactly one should have succeeded
	successCount := 0
	for _, success := range results {
		if success {
			successCount++
		}
	}

	assert.Equal(t, 1, successCount, "Exactly one instance should become leader")
}

// testInstance wraps coordination components for integration testing.
type testInstance struct {
	id       string
	registry *InstanceRegistry
	leader   *LeaderElection
	engine   *mockEngine
	pubsub   *ConfigInvalidator
}

func newTestInstance(t *testing.T, redisClient *redis.Client, id int) *testInstance {
	t.Helper()

	instanceID := string(rune('A' + id - 1))
	engine := &mockEngine{invalidations: []uuid.UUID{}}

	return &testInstance{
		id:       instanceID,
		registry: NewInstanceRegistry(redisClient, instanceID, 100*time.Millisecond, "v1.0.0"),
		leader:   NewLeaderElection(redisClient, instanceID, "leader:test", 5*time.Second),
		engine:   engine,
		pubsub:   NewConfigInvalidator(redisClient, engine),
	}
}

func (inst *testInstance) start(ctx context.Context) {
	// Try to become leader once (don't retry)
	_, _ = inst.leader.TryBecomeLeader(ctx)

	// Start registry and pub/sub
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.registry.Start(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.pubsub.Start(ctx)
	}()

	wg.Wait()
}
