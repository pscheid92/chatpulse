package app

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	redistest "github.com/testcontainers/testcontainers-go/modules/redis"
)

func setupTestRedis(t *testing.T) *goredis.Client {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()

	// Start Redis container
	container, err := redistest.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, container.Terminate(ctx))
	})

	// Get connection string
	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	// Create client
	opts, err := goredis.ParseURL(connStr)
	require.NoError(t, err)

	client := goredis.NewClient(opts)

	// Verify connection
	require.NoError(t, client.Ping(ctx).Err())

	// Flush all data before test
	require.NoError(t, client.FlushAll(ctx).Err())

	return client
}

func TestLeaderElector_TryAcquire_SingleInstance(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector := NewLeaderElector(rdb, "instance-1")

	// First acquisition should succeed
	acquired, err := elector.TryAcquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired, "first instance should acquire leadership")

	// Verify key exists in Redis
	val, err := rdb.Get(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Equal(t, "instance-1", val)

	// Verify TTL is set
	ttl, err := rdb.TTL(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl.Seconds(), 20.0, "TTL should be ~30s")
	assert.LessOrEqual(t, ttl.Seconds(), 30.0)
}

func TestLeaderElector_TryAcquire_MultipleInstances(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector1 := NewLeaderElector(rdb, "instance-1")
	elector2 := NewLeaderElector(rdb, "instance-2")
	elector3 := NewLeaderElector(rdb, "instance-3")

	// Instance 1 becomes leader
	acquired1, err := elector1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired1)

	// Instances 2 and 3 should fail to acquire
	acquired2, err := elector2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired2, "instance 2 should NOT become leader")

	acquired3, err := elector3.TryAcquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired3, "instance 3 should NOT become leader")

	// Verify instance 1 is still the leader
	val, err := rdb.Get(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Equal(t, "instance-1", val)
}

func TestLeaderElector_Renew_Success(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector := NewLeaderElector(rdb, "instance-1")

	// Acquire leadership
	acquired, err := elector.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Wait 2 seconds
	time.Sleep(2 * time.Second)

	// Renew should succeed
	err = elector.Renew(ctx)
	require.NoError(t, err)

	// Verify TTL was refreshed
	ttl, err := rdb.TTL(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Greater(t, ttl.Seconds(), 25.0, "TTL should be refreshed to ~30s")
}

func TestLeaderElector_Renew_LockLost(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector := NewLeaderElector(rdb, "instance-1")

	// Acquire leadership
	acquired, err := elector.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Simulate lock expiry
	err = rdb.Del(ctx, "cleanup:leader").Err()
	require.NoError(t, err)

	// Renew should fail
	err = elector.Renew(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leader lock lost")
}

func TestLeaderElector_Renew_LockStolen(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector1 := NewLeaderElector(rdb, "instance-1")

	// Instance 1 becomes leader
	acquired, err := elector1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Simulate lock stolen (force instance 2 to become leader)
	err = rdb.Set(ctx, "cleanup:leader", "instance-2", 30*time.Second).Err()
	require.NoError(t, err)

	// Instance 1 tries to renew - should fail
	err = elector1.Renew(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leader lock stolen by instance-2")
}

func TestLeaderElector_Release_Success(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector := NewLeaderElector(rdb, "instance-1")

	// Acquire leadership
	acquired, err := elector.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Release leadership
	err = elector.Release(ctx)
	require.NoError(t, err)

	// Verify key is deleted
	_, err = rdb.Get(ctx, "cleanup:leader").Result()
	assert.ErrorIs(t, err, goredis.Nil, "lock key should be deleted")
}

func TestLeaderElector_Release_NotLeader(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector1 := NewLeaderElector(rdb, "instance-1")
	elector2 := NewLeaderElector(rdb, "instance-2")

	// Instance 1 becomes leader
	acquired, err := elector1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Instance 2 tries to release (should be a no-op, not delete instance 1's lock)
	err = elector2.Release(ctx)
	require.NoError(t, err)

	// Verify instance 1 is still the leader
	val, err := rdb.Get(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Equal(t, "instance-1", val, "instance 1 should still be leader")
}

func TestLeaderElector_Failover_AfterTTLExpiry(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector1 := NewLeaderElector(rdb, "instance-1")

	// Instance 1 becomes leader
	acquired, err := elector1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Wait for TTL to expire (use short TTL for test speed)
	elector1.lockTTL = 2 * time.Second
	err = rdb.Set(ctx, "cleanup:leader", "instance-1", elector1.lockTTL).Err()
	require.NoError(t, err)

	time.Sleep(3 * time.Second)

	// Instance 2 should be able to become leader
	elector2 := NewLeaderElector(rdb, "instance-2")
	acquired, err = elector2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired, "instance 2 should become leader after TTL expiry")

	// Verify instance 2 is now the leader
	val, err := rdb.Get(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Equal(t, "instance-2", val)
}

func TestLeaderElector_RenewalPreventsTakeover(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector1 := NewLeaderElector(rdb, "instance-1")

	// Instance 1 becomes leader
	acquired, err := elector1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Start renewal goroutine
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := elector1.Renew(ctx); err != nil {
					return
				}
			case <-stopRenewal:
				return
			}
		}
	}()

	// Wait 2 seconds (renewals should keep lock alive)
	time.Sleep(2 * time.Second)

	// Instance 2 tries to become leader - should fail
	elector2 := NewLeaderElector(rdb, "instance-2")
	acquired, err = elector2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired, "instance 2 should NOT become leader (lease renewed)")

	// Verify instance 1 is still the leader
	val, err := rdb.Get(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Equal(t, "instance-1", val)

	// Cleanup
	close(stopRenewal)
	<-renewalDone
}

func TestLeaderElector_GracefulRelease_ImmediateTakeover(t *testing.T) {
	rdb := setupTestRedis(t)
	ctx := context.Background()

	elector1 := NewLeaderElector(rdb, "instance-1")
	elector2 := NewLeaderElector(rdb, "instance-2")

	// Instance 1 becomes leader
	acquired, err := elector1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	// Instance 1 releases leadership
	err = elector1.Release(ctx)
	require.NoError(t, err)

	// Instance 2 can immediately become leader (no waiting for TTL)
	acquired, err = elector2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired, "instance 2 should become leader immediately after release")

	// Verify instance 2 is now the leader
	val, err := rdb.Get(ctx, "cleanup:leader").Result()
	require.NoError(t, err)
	assert.Equal(t, "instance-2", val)
}
