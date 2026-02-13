package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreakerHook_NormalOperation(t *testing.T) {
	hook := NewCircuitBreakerHook()

	// Circuit should start in closed state
	assert.Equal(t, circuitbreaker.ClosedState, hook.GetState())

	// Simulate successful operations
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return nil
		})
		err := processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
		assert.NoError(t, err)
	}

	// Circuit should remain closed
	assert.Equal(t, circuitbreaker.ClosedState, hook.GetState())
	m := hook.GetMetrics()
	assert.Equal(t, uint(10), m.Executions())
	assert.Equal(t, uint(10), m.Successes())
	assert.Equal(t, uint(0), m.Failures())
}

func TestCircuitBreakerHook_TransientFailures(t *testing.T) {
	hook := NewCircuitBreakerHook()

	ctx := context.Background()

	// Simulate 2 failures (below threshold of 5 requests)
	for i := 0; i < 2; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("connection refused")
		})
		err := processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
		assert.Error(t, err)
		assert.NotErrorIs(t, err, circuitbreaker.ErrOpen)
	}

	// Circuit should remain closed (not enough requests to trip)
	assert.Equal(t, circuitbreaker.ClosedState, hook.GetState())
}

func TestCircuitBreakerHook_OpensAfterSustainedFailures(t *testing.T) {
	hook := NewCircuitBreakerHook()

	ctx := context.Background()

	// Simulate 5 consecutive failures (meets threshold)
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("connection timeout")
		})
		err := processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
		assert.Error(t, err)
	}

	// Circuit should now be open
	assert.Equal(t, circuitbreaker.OpenState, hook.GetState())
}

func TestCircuitBreakerHook_FailsFastWhenOpen(t *testing.T) {
	hook := NewCircuitBreakerHook()

	ctx := context.Background()

	// Trip the circuit breaker (5 failures)
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("redis down")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
	}

	// Circuit should be open
	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Next request should fail fast without calling Redis
	called := false
	processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
		called = true
		return nil
	})

	cmd := goredis.NewStringCmd(ctx, "set", "key", "value")
	err := processHook(ctx, cmd)

	// Should return circuit breaker error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
	// Redis should not have been called
	assert.False(t, called, "Redis should not be called when circuit is open")
}

func TestCircuitBreakerHook_RecoveryToHalfOpen(t *testing.T) {
	// Create hook with very short delay for testing
	cb := circuitbreaker.NewBuilder[any]().
		WithFailureThresholdRatio(3, 3).
		WithDelay(100 * time.Millisecond). // Very short delay for test
		WithSuccessThreshold(3).
		Build()

	hook := &CircuitBreakerHook{
		cb: cb,
		cache: &cacheStore{
			values: make(map[string]cachedValue),
		},
	}

	ctx := context.Background()

	// Trip the circuit (3 failures)
	for i := 0; i < 3; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("failure")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Wait for delay
	time.Sleep(150 * time.Millisecond)

	// Next request should enter half-open state
	processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
		return nil // Success
	})
	err := processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
	assert.NoError(t, err)

	// Circuit should be in half-open state
	assert.Equal(t, circuitbreaker.HalfOpenState, hook.GetState())
}

func TestCircuitBreakerHook_ClosesAfterSuccessfulRecovery(t *testing.T) {
	// Create hook with very short delay
	cb := circuitbreaker.NewBuilder[any]().
		WithFailureThresholdRatio(3, 3).
		WithDelay(100 * time.Millisecond).
		WithSuccessThreshold(3).
		Build()

	hook := &CircuitBreakerHook{
		cb: cb,
		cache: &cacheStore{
			values: make(map[string]cachedValue),
		},
	}

	ctx := context.Background()

	// Trip the circuit
	for i := 0; i < 3; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("failure")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Wait for delay → half-open
	time.Sleep(150 * time.Millisecond)

	// Make 3 successful requests (SuccessThreshold)
	for i := 0; i < 3; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return nil
		})
		err := processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
		require.NoError(t, err)
	}

	// Circuit should be closed again
	assert.Equal(t, circuitbreaker.ClosedState, hook.GetState())
}

func TestCircuitBreakerHook_CachesFallback(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// First, make a successful GET request
	processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
		// Simulate successful GET
		if stringCmd, ok := cmd.(*goredis.StringCmd); ok {
			stringCmd.SetVal("cached-value")
		}
		return nil
	})

	cmd := goredis.NewStringCmd(ctx, "get", "test-key")
	err := processHook(ctx, cmd)
	require.NoError(t, err)

	// Value should be cached
	cached := hook.cache.values["test-key"]
	assert.Equal(t, "cached-value", cached.data)
	assert.WithinDuration(t, time.Now(), cached.timestamp, 1*time.Second)
}

func TestCircuitBreakerHook_ServesCachedValueWhenOpen(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// Cache a value
	hook.cache.mu.Lock()
	hook.cache.values["test-key"] = cachedValue{
		data:      "stale-value",
		timestamp: time.Now(),
	}
	hook.cache.mu.Unlock()

	// Trip the circuit
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("redis down")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "set", "key", "value"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Try to GET the cached key
	processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
		// This should not be called
		t.Fatal("Redis should not be called when circuit is open")
		return nil
	})

	cmd := goredis.NewStringCmd(ctx, "get", "test-key")
	err := processHook(ctx, cmd)

	// Should succeed with cached value
	assert.NoError(t, err)
	result, _ := cmd.Result()
	assert.Equal(t, "stale-value", result)
}

func TestCircuitBreakerHook_CacheExpiry(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// Cache a value with old timestamp (expired)
	hook.cache.mu.Lock()
	hook.cache.values["expired-key"] = cachedValue{
		data:      "old-value",
		timestamp: time.Now().Add(-10 * time.Minute), // Expired (TTL is 5 min)
	}
	hook.cache.mu.Unlock()

	// Trip the circuit
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("redis down")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "set", "key", "value"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Try to GET the expired key
	processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
		return nil
	})

	cmd := goredis.NewStringCmd(ctx, "get", "expired-key")
	err := processHook(ctx, cmd)

	// Should fail because cache is expired
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
}

func TestCircuitBreakerHook_ReadOnlyFunctionFailsWhenOpen(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// Trip the circuit
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("redis down")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "set", "key", "value"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Try to call FCALL_RO for get_decayed_value (sentiment read)
	processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
		t.Fatal("Redis should not be called")
		return nil
	})

	cmd := goredis.NewCmd(ctx, "fcall_ro", "get_decayed_value", "1", "session:uuid", "1.0", "123456")
	err := processHook(ctx, cmd)

	// Should return error — no fake 0.0 fallback
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
}

func TestCircuitBreakerHook_WriteOperationsFailWhenOpen(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// Trip the circuit
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("redis down")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Try write operations (SET, HSET, FCALL)
	writeCommands := [][]any{
		{"set", "key", "value"},
		{"hset", "hash", "field", "value"},
		{"fcall", "apply_vote", "1", "session:uuid", "10", "1.0", "123456"},
	}

	for _, cmdArgs := range writeCommands {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			t.Fatal("Redis should not be called")
			return nil
		})

		cmd := goredis.NewCmd(ctx, cmdArgs...)
		err := processHook(ctx, cmd)

		// Should fail with circuit breaker error
		assert.Error(t, err, "Command %v should fail when circuit open", cmdArgs[0])
		assert.Contains(t, err.Error(), "circuit breaker open")
	}
}

func TestCircuitBreakerHook_PipelineFailsWhenOpen(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// Trip the circuit
	for i := 0; i < 5; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return errors.New("redis down")
		})
		_ = processHook(ctx, goredis.NewStringCmd(ctx, "get", "key"))
	}

	require.Equal(t, circuitbreaker.OpenState, hook.GetState())

	// Try pipeline operation
	pipelineHook := hook.ProcessPipelineHook(func(ctx context.Context, cmds []goredis.Cmder) error {
		t.Fatal("Redis pipeline should not be called")
		return nil
	})

	cmds := []goredis.Cmder{
		goredis.NewStringCmd(ctx, "get", "key1"),
		goredis.NewStringCmd(ctx, "get", "key2"),
	}
	err := pipelineHook(ctx, cmds)

	// Should fail with circuit breaker error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
}

func TestStateToFloat(t *testing.T) {
	tests := []struct {
		state    circuitbreaker.State
		expected float64
		name     string
	}{
		{circuitbreaker.ClosedState, 0, "closed"},
		{circuitbreaker.HalfOpenState, 1, "half-open"},
		{circuitbreaker.OpenState, 2, "open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stateToFloat(tt.state)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCircuitBreakerHook_RedisNilIsNotFailure(t *testing.T) {
	hook := NewCircuitBreakerHook()
	ctx := context.Background()

	// Simulate many redis.Nil responses (cache misses)
	for i := 0; i < 10; i++ {
		processHook := hook.ProcessHook(func(ctx context.Context, cmd goredis.Cmder) error {
			return goredis.Nil
		})
		err := processHook(ctx, goredis.NewStringCmd(ctx, "get", "missing-key"))
		// redis.Nil gets wrapped in our error format
		assert.Error(t, err)
	}

	// Circuit should remain closed — redis.Nil is not a failure
	assert.Equal(t, circuitbreaker.ClosedState, hook.GetState())
	m := hook.GetMetrics()
	assert.Equal(t, uint(0), m.Failures())
	assert.Equal(t, uint(10), m.Successes())
}
