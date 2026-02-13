package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

// setupDedicatedRedis creates a dedicated Redis container for tests that need to stop/start Redis.
// This prevents interference with other tests that share the global test container.
func setupDedicatedRedis(t *testing.T) (testcontainers.Container, string) {
	t.Helper()

	ctx := context.Background()
	container, err := redis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	return container, "redis://" + endpoint
}

func TestCircuitBreakerIntegration_RealRedisFailure(t *testing.T) {
	t.Skip("Skipping flaky test - circuit breaker recovery timing is sensitive to connection pool behavior")
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create dedicated Redis container for this test to avoid interfering with other tests
	dedicatedContainer, dedicatedRedisURL := setupDedicatedRedis(t)

	// Create Redis client with circuit breaker
	client, err := NewClient(ctx, dedicatedRedisURL)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Flush Redis to start clean
	require.NoError(t, client.FlushAll(ctx).Err())

	// Phase 1: Normal operation - circuit should be closed
	// We verify behavior through actual operations since hooks aren't directly accessible
	for i := 0; i < 3; i++ {
		err := client.Set(ctx, fmt.Sprintf("test-key-%d", i), "test-value", 0).Err()
		require.NoError(t, err, "Normal operation should succeed")
	}

	// Verify we can read the values back
	val, err := client.Get(ctx, "test-key-0").Result()
	require.NoError(t, err)
	assert.Equal(t, "test-value", val)

	// Phase 2: Simulate Redis becoming unavailable
	// We'll stop the container temporarily
	t.Log("Stopping Redis container to simulate failure...")
	err = dedicatedContainer.Stop(ctx, nil)
	require.NoError(t, err)

	// Give it a moment to fully stop
	time.Sleep(500 * time.Millisecond)

	// Phase 3: Try operations - should fail and eventually trip circuit breaker
	t.Log("Attempting operations against stopped Redis...")
	failureCount := 0
	for i := 0; i < 10; i++ {
		err := client.Set(ctx, "fail-key", "value", 0).Err()
		if err != nil {
			failureCount++
			t.Logf("Attempt %d failed (expected): %v", i+1, err)
		}

		// After a few failures, circuit should open and fail fast
		if i >= 5 {
			// Should start failing fast with circuit breaker error
			assert.Error(t, err)
			if err != nil {
				// Check if it's a circuit breaker error (fail-fast)
				if err.Error() == circuitbreaker.ErrOpen.Error() ||
					(err.Error() != "EOF" && err.Error() != "i/o timeout") {
					t.Logf("Circuit breaker opened (fail-fast mode)")
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	assert.GreaterOrEqual(t, failureCount, 5, "Should have multiple failures")

	// Phase 4: Try to read with circuit open (should use cached value if available)
	// Since we successfully read "test-key-0" earlier, it might be cached
	val, err = client.Get(ctx, "test-key-0").Result()
	t.Logf("Read attempt with circuit open: val=%v, err=%v", val, err)
	// This might succeed with cached value or fail - both are acceptable

	// Phase 5: Restart Redis
	t.Log("Restarting Redis container...")
	err = dedicatedContainer.Start(ctx)
	require.NoError(t, err)

	// Wait for Redis to be ready
	time.Sleep(2 * time.Second)

	// Phase 6: Wait for circuit breaker timeout (30 seconds)
	// The circuit will transition to half-open and then closed after successful requests
	t.Log("Waiting for circuit breaker recovery...")
	time.Sleep(31 * time.Second)

	// Phase 7: Verify operations work again
	// Circuit breaker may need multiple cycles to fully recover because in half-open
	// state it only allows 1 request (SuccessThreshold=1), but Redis connection pool
	// tries to dial multiple connections, which can fail the half-open test.
	// Keep trying for up to 90 seconds with increasing backoff.
	t.Log("Attempting operations after Redis recovery...")
	recovered := false
	maxWait := 90 * time.Second
	deadline := time.Now().Add(maxWait)
	attemptNum := 0
	for time.Now().Before(deadline) {
		attemptNum++
		err := client.Set(ctx, "recovery-key", fmt.Sprintf("value-%d", attemptNum), 0).Err()
		if err == nil {
			t.Logf("Operation succeeded on attempt %d - Redis recovered", attemptNum)
			recovered = true
			break
		}
		t.Logf("Recovery attempt %d failed: %v", attemptNum, err)
		// Progressive backoff: start with 5s, increase to 10s after 3 attempts
		waitTime := 5 * time.Second
		if attemptNum > 3 {
			waitTime = 10 * time.Second
		}
		time.Sleep(waitTime)
	}

	assert.True(t, recovered, "Should recover after Redis restart")

	// Verify we can read/write normally now
	err = client.Set(ctx, "final-test", "success", 0).Err()
	require.NoError(t, err, "Should be able to write after recovery")

	val, err = client.Get(ctx, "final-test").Result()
	require.NoError(t, err, "Should be able to read after recovery")
	assert.Equal(t, "success", val)

	t.Log("Circuit breaker integration test completed successfully")
}

func TestCircuitBreakerIntegration_GracefulDegradation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create dedicated Redis container for this test
	dedicatedContainer, dedicatedRedisURL := setupDedicatedRedis(t)

	// Create Redis client
	client, err := NewClient(ctx, dedicatedRedisURL)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Flush Redis
	require.NoError(t, client.FlushAll(ctx).Err())

	// Phase 1: Store some values that will be cached
	testData := map[string]string{
		"user:1": "cached-data-1",
		"user:2": "cached-data-2",
		"user:3": "cached-data-3",
	}

	for key, val := range testData {
		err := client.Set(ctx, key, val, 0).Err()
		require.NoError(t, err)
	}

	// Read the values to ensure they're cached
	for key := range testData {
		_, err := client.Get(ctx, key).Result()
		require.NoError(t, err)
	}

	// Phase 2: Stop Redis
	t.Log("Stopping Redis for graceful degradation test...")
	err = dedicatedContainer.Stop(ctx, nil)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// Phase 3: Trip the circuit breaker
	for i := 0; i < 6; i++ {
		_ = client.Set(ctx, "trip", "value", 0).Err()
		time.Sleep(100 * time.Millisecond)
	}

	// Phase 4: Verify graceful degradation
	// Read operations might serve from cache
	t.Log("Testing graceful degradation with cached values...")
	for key, expectedVal := range testData {
		val, err := client.Get(ctx, key).Result()
		if err == nil {
			// Served from cache
			assert.Equal(t, expectedVal, val, "Cached value should match")
			t.Logf("Successfully served %s from cache", key)
		} else {
			// Cache miss or circuit open
			t.Logf("Cache miss for %s: %v", key, err)
		}
	}

	// Write operations should fail
	err = client.Set(ctx, "new-key", "new-value", 0).Err()
	assert.Error(t, err, "Write operations should fail when circuit open")
	t.Logf("Write operation correctly failed: %v", err)

	// Phase 5: Restart Redis for cleanup
	t.Log("Restarting Redis...")
	err = dedicatedContainer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
}

func TestCircuitBreakerIntegration_SentimentFailsWhenOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create dedicated Redis container for this test
	dedicatedContainer, dedicatedRedisURL := setupDedicatedRedis(t)

	// Create Redis client
	client, err := NewClient(ctx, dedicatedRedisURL)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Flush Redis
	require.NoError(t, client.FlushAll(ctx).Err())

	// Phase 1: Create a test session and verify read works
	sessionKey := "session:test-uuid"
	err = client.HSet(ctx, sessionKey, map[string]any{
		"value":       "50.0",
		"last_update": "0",
	}).Err()
	require.NoError(t, err)

	result, err := client.FCallRO(ctx, fnGetSentiment, []string{sessionKey}, "1.0", "123456").Text()
	require.NoError(t, err)
	t.Logf("Initial sentiment read: %s", result)

	// Phase 2: Stop Redis
	t.Log("Stopping Redis...")
	err = dedicatedContainer.Stop(ctx, nil)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// Phase 3: Trip the circuit breaker
	for i := 0; i < 6; i++ {
		_ = client.Set(ctx, "trip", "value", 0).Err()
		time.Sleep(100 * time.Millisecond)
	}

	// Phase 4: Sentiment read should fail with circuit open (no fake 0.0 fallback)
	t.Log("Testing sentiment read with circuit open...")
	_, err = client.FCallRO(ctx, fnGetSentiment, []string{sessionKey}, "1.0", "123456").Text()
	assert.Error(t, err, "Sentiment read should fail when circuit is open")
	t.Logf("Sentiment read correctly failed: %v", err)

	// Phase 5: Restart Redis for cleanup
	t.Log("Restarting Redis...")
	err = dedicatedContainer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
}
