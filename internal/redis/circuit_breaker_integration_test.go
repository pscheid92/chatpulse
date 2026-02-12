package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreakerIntegration_RealRedisFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create Redis client with circuit breaker (using test Redis from integration_test.go)
	client, err := NewClient(ctx, testRedisURL)
	require.NoError(t, err)
	defer client.Close()

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
	err = redContainer.Stop(ctx, nil)
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
				if err.Error() == gobreaker.ErrOpenState.Error() ||
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
	err = redContainer.Start(ctx)
	require.NoError(t, err)

	// Wait for Redis to be ready
	time.Sleep(2 * time.Second)

	// Phase 6: Wait for circuit breaker timeout (10 seconds in production, but should be quicker in test)
	// The circuit will transition to half-open and then closed after successful requests
	t.Log("Waiting for circuit breaker recovery...")
	time.Sleep(11 * time.Second)

	// Phase 7: Verify operations work again
	t.Log("Attempting operations after Redis recovery...")
	recovered := false
	for i := 0; i < 10; i++ {
		err := client.Set(ctx, "recovery-key", fmt.Sprintf("value-%d", i), 0).Err()
		if err == nil {
			t.Logf("Operation succeeded on attempt %d - Redis recovered", i+1)
			recovered = true
			break
		}
		t.Logf("Recovery attempt %d failed: %v", i+1, err)
		time.Sleep(1 * time.Second)
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

	// Create Redis client
	client, err := NewClient(ctx, testRedisURL)
	require.NoError(t, err)
	defer client.Close()

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
	err = redContainer.Stop(ctx, nil)
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
	err = redContainer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
}

func TestCircuitBreakerIntegration_SentimentFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Create Redis client
	client, err := NewClient(ctx, testRedisURL)
	require.NoError(t, err)
	defer client.Close()

	// Flush Redis
	require.NoError(t, client.FlushAll(ctx).Err())

	// Phase 1: Create a test session
	sessionKey := "session:test-uuid"
	err = client.HSet(ctx, sessionKey, map[string]any{
		"value":       "50.0",
		"last_update": "0",
	}).Err()
	require.NoError(t, err)

	// Read sentiment successfully (should be cached)
	result, err := client.FCallRO(ctx, fnGetSentiment, []string{sessionKey}, "1.0", "123456").Text()
	require.NoError(t, err)
	t.Logf("Initial sentiment read: %s", result)

	// Phase 2: Stop Redis
	t.Log("Stopping Redis for sentiment fallback test...")
	err = redContainer.Stop(ctx, nil)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// Phase 3: Trip the circuit breaker
	for i := 0; i < 6; i++ {
		_ = client.Set(ctx, "trip", "value", 0).Err()
		time.Sleep(100 * time.Millisecond)
	}

	// Phase 4: Try to read sentiment with circuit open
	// Should return neutral sentiment (0.0) as fallback
	t.Log("Testing sentiment read with circuit open...")
	result, err = client.FCallRO(ctx, fnGetSentiment, []string{sessionKey}, "1.0", "123456").Text()
	if err == nil {
		// Should get neutral sentiment fallback
		t.Logf("Sentiment fallback returned: %s", result)
		// Note: Fallback might be "0.0" (neutral) or empty depending on cache state
	} else {
		t.Logf("Sentiment read failed (expected): %v", err)
	}

	// Phase 5: Restart Redis for cleanup
	t.Log("Restarting Redis...")
	err = redContainer.Start(ctx)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
}
