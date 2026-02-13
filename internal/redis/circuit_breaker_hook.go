package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/pscheid92/chatpulse/internal/metrics"
	goredis "github.com/redis/go-redis/v9"
)

// CircuitBreakerHook implements redis.Hook to add circuit breaker protection
// to all Redis operations. This prevents cascading failures when Redis becomes
// unavailable or slow.
//
// Architecture Decision: We use the hooks pattern (rather than wrapping the client)
// because it leverages the existing hooks infrastructure (MetricsHook), provides
// automatic coverage of all Redis operations, and maintains a cleaner, more
// maintainable architecture.
type CircuitBreakerHook struct {
	cb    circuitbreaker.CircuitBreaker[any]
	cache *cacheStore
}

var _ goredis.Hook = (*CircuitBreakerHook)(nil)

// cacheStore holds cached values for fallback when circuit is open
type cacheStore struct {
	mu     sync.RWMutex
	values map[string]cachedValue
}

type cachedValue struct {
	data      string
	timestamp time.Time
}

const cacheTTL = 5 * time.Minute

// NewCircuitBreakerHook creates a new circuit breaker hook with the following settings:
// - WithFailureRateThreshold: 60% failure rate, min 5 requests, 10s rolling window
// - WithDelay: 30s before transitioning from open to half-open
// - WithSuccessThreshold: 1 successful request in half-open to close
func NewCircuitBreakerHook() *CircuitBreakerHook {
	cb := circuitbreaker.NewBuilder[any]().
		WithFailureRateThreshold(0.6, 5, 10*time.Second).
		WithDelay(30 * time.Second).
		WithSuccessThreshold(1).
		OnStateChanged(func(e circuitbreaker.StateChangedEvent) {
			slog.Warn("Circuit breaker state changed",
				"component", "redis",
				"from", e.OldState.String(),
				"to", e.NewState.String(),
			)

			// Update metrics
			metrics.CircuitBreakerStateChanges.WithLabelValues("redis", e.NewState.String()).Inc()
			metrics.CircuitBreakerState.WithLabelValues("redis").Set(stateToFloat(e.NewState))
		}).
		Build()

	return &CircuitBreakerHook{
		cb: cb,
		cache: &cacheStore{
			values: make(map[string]cachedValue),
		},
	}
}

func stateToFloat(state circuitbreaker.State) float64 {
	switch state {
	case circuitbreaker.ClosedState:
		return 0
	case circuitbreaker.HalfOpenState:
		return 1
	case circuitbreaker.OpenState:
		return 2
	default:
		return -1
	}
}

// DialHook wraps connection establishment with circuit breaker
func (h *CircuitBreakerHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !h.cb.TryAcquirePermit() {
			return nil, fmt.Errorf("circuit breaker dial failed: %w", circuitbreaker.ErrOpen)
		}
		conn, err := next(ctx, network, addr)
		if err != nil {
			h.cb.RecordError(err)
			return nil, fmt.Errorf("circuit breaker dial failed: %w", err)
		}
		h.cb.RecordSuccess()
		return conn, nil
	}
}

// ProcessHook wraps command execution with circuit breaker and caching
func (h *CircuitBreakerHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		if !h.cb.TryAcquirePermit() {
			return h.handleFallback(cmd)
		}

		err := next(ctx, cmd)
		if err != nil && !errors.Is(err, goredis.Nil) {
			h.cb.RecordError(err)
		} else {
			h.cb.RecordSuccess()
		}

		// Cache successful results for future fallback
		if err == nil || errors.Is(err, goredis.Nil) {
			h.cacheResult(cmd)
		}

		if err != nil {
			return fmt.Errorf("circuit breaker process failed: %w", err)
		}
		return nil
	}
}

// ProcessPipelineHook wraps pipeline execution with circuit breaker
func (h *CircuitBreakerHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		if !h.cb.TryAcquirePermit() {
			return fmt.Errorf("redis circuit breaker open: %w", circuitbreaker.ErrOpen)
		}

		err := next(ctx, cmds)
		if err != nil {
			h.cb.RecordError(err)
			return fmt.Errorf("circuit breaker pipeline failed: %w", err)
		}
		h.cb.RecordSuccess()
		return nil
	}
}

// handleFallback attempts to serve cached data when circuit is open
func (h *CircuitBreakerHook) handleFallback(cmd goredis.Cmder) error {
	cmdName := cmd.Name()

	switch cmdName {
	case "hget", "get":
		// Try to serve from cache for read operations
		if result := h.getFromCache(cmd); result != "" {
			slog.Debug("Circuit breaker open, serving from cache",
				"command", cmdName,
				"args", cmd.Args(),
			)
			// Set the cached value into the command result
			// This is a bit of a hack but works with redis.Cmder interface
			if c, ok := cmd.(*goredis.StringCmd); ok {
				c.SetVal(result)
				return nil
			}
		}
		// No cache available
		return fmt.Errorf("redis circuit breaker open and no cached value: %w", circuitbreaker.ErrOpen)

	case "fcall_ro":
		// Read-only functions can potentially use cached values
		// For sentiment reads (get_decayed_value), return neutral value as safe fallback
		if len(cmd.Args()) > 1 && cmd.Args()[1] == fnGetSentiment {
			slog.Warn("Circuit breaker open for sentiment read, returning neutral value",
				"args", cmd.Args(),
			)
			// Return "0.0" as neutral sentiment
			if c, ok := cmd.(*goredis.Cmd); ok {
				c.SetVal("0.0")
				return nil
			}
		}
		return fmt.Errorf("redis circuit breaker open: %w", circuitbreaker.ErrOpen)

	case "hset", "set", "fcall":
		// Write operations cannot be served from cache
		// These will fail, but that's expected behavior
		slog.Warn("Circuit breaker open for write operation",
			"command", cmdName,
			"args", cmd.Args(),
		)
		return fmt.Errorf("redis circuit breaker open: %w", circuitbreaker.ErrOpen)

	default:
		// Unknown command - fail fast
		return fmt.Errorf("redis circuit breaker open: %w", circuitbreaker.ErrOpen)
	}
}

// cacheResult stores successful read results for future fallback
func (h *CircuitBreakerHook) cacheResult(cmd goredis.Cmder) {
	cmdName := cmd.Name()

	// Only cache read operations
	switch cmdName {
	case "hget", "get":
		if err := cmd.Err(); err != nil && !errors.Is(err, goredis.Nil) {
			return
		}

		// Extract the key and value
		args := cmd.Args()
		if len(args) < 2 {
			return
		}

		key := fmt.Sprintf("%v", args[1])
		value := ""

		if c, ok := cmd.(*goredis.StringCmd); ok {
			value, _ = c.Result()
		}

		if value != "" {
			h.cache.mu.Lock()
			h.cache.values[key] = cachedValue{
				data:      value,
				timestamp: time.Now(),
			}
			h.cache.mu.Unlock()
		}

	case "fcall_ro":
		// Cache read-only function results
		if len(cmd.Args()) > 1 && cmd.Args()[1] == fnGetSentiment {
			// Cache sentiment values
			if c, ok := cmd.(*goredis.Cmd); ok {
				if val, err := c.Text(); err == nil && val != "" {
					// Use session key as cache key
					if len(cmd.Args()) > 3 {
						key := fmt.Sprintf("sentiment:%v", cmd.Args()[3])
						h.cache.mu.Lock()
						h.cache.values[key] = cachedValue{
							data:      val,
							timestamp: time.Now(),
						}
						h.cache.mu.Unlock()
					}
				}
			}
		}
	}
}

// getFromCache retrieves a cached value if available and not expired
func (h *CircuitBreakerHook) getFromCache(cmd goredis.Cmder) string {
	args := cmd.Args()
	if len(args) < 2 {
		return ""
	}

	key := fmt.Sprintf("%v", args[1])

	h.cache.mu.RLock()
	defer h.cache.mu.RUnlock()

	cached, ok := h.cache.values[key]
	if !ok {
		return ""
	}

	// Check if cache entry is still valid (within TTL)
	if time.Since(cached.timestamp) > cacheTTL {
		// Cache expired
		return ""
	}

	return cached.data
}

// GetState returns the current state of the circuit breaker (for testing/monitoring)
func (h *CircuitBreakerHook) GetState() circuitbreaker.State {
	return h.cb.State()
}

// GetMetrics returns the current metrics (for testing/monitoring)
func (h *CircuitBreakerHook) GetMetrics() circuitbreaker.Metrics {
	return h.cb.Metrics()
}
