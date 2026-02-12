package redis

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pscheid92/chatpulse/internal/metrics"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker"
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
	cb    *gobreaker.CircuitBreaker
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
// - MaxRequests: 3 (half-open allows 3 test requests)
// - Interval: 60s (rolling window for failure counting)
// - Timeout: 10s (how long circuit stays open before half-open)
// - ReadyToTrip: Opens after 5 requests with 60% failure rate
func NewCircuitBreakerHook() *CircuitBreakerHook {
	settings := gobreaker.Settings{
		Name:        "redis",
		MaxRequests: 3, // In half-open, allow 3 test requests
		Interval:    60 * time.Second,
		Timeout:     10 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// Open circuit if we have at least 5 requests and 60% are failures
			if counts.Requests < 5 {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= 0.6
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Warn("Circuit breaker state changed",
				"component", name,
				"from", from.String(),
				"to", to.String(),
			)

			// Update metrics
			metrics.CircuitBreakerStateChanges.WithLabelValues(name, to.String()).Inc()
			metrics.CircuitBreakerState.WithLabelValues(name).Set(stateToFloat(to))
		},
	}

	return &CircuitBreakerHook{
		cb: gobreaker.NewCircuitBreaker(settings),
		cache: &cacheStore{
			values: make(map[string]cachedValue),
		},
	}
}

func stateToFloat(state gobreaker.State) float64 {
	switch state {
	case gobreaker.StateClosed:
		return 0
	case gobreaker.StateHalfOpen:
		return 1
	case gobreaker.StateOpen:
		return 2
	default:
		return -1
	}
}

// DialHook wraps connection establishment with circuit breaker
func (h *CircuitBreakerHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		result, err := h.cb.Execute(func() (interface{}, error) {
			return next(ctx, network, addr)
		})
		if err != nil {
			return nil, err
		}
		return result.(net.Conn), nil
	}
}

// ProcessHook wraps command execution with circuit breaker and caching
func (h *CircuitBreakerHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		_, err := h.cb.Execute(func() (interface{}, error) {
			return nil, next(ctx, cmd)
		})

		if err == gobreaker.ErrOpenState {
			// Circuit is open - try fallback behavior
			return h.handleFallback(cmd)
		}

		// Success - cache the result for future fallback
		if err == nil || err == goredis.Nil {
			h.cacheResult(cmd)
		}

		return err
	}
}

// ProcessPipelineHook wraps pipeline execution with circuit breaker
func (h *CircuitBreakerHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		_, err := h.cb.Execute(func() (interface{}, error) {
			return nil, next(ctx, cmds)
		})

		if err == gobreaker.ErrOpenState {
			// Circuit is open - pipeline operations have no good fallback
			// Return error so caller can handle appropriately
			return fmt.Errorf("redis circuit breaker open: %w", err)
		}

		return err
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
			switch c := cmd.(type) {
			case *goredis.StringCmd:
				c.SetVal(result)
				return nil
			}
		}
		// No cache available
		return fmt.Errorf("redis circuit breaker open and no cached value: %w", gobreaker.ErrOpenState)

	case "fcall_ro":
		// Read-only functions can potentially use cached values
		// For sentiment reads (get_decayed_value), return neutral value as safe fallback
		if len(cmd.Args()) > 1 && cmd.Args()[1] == fnGetSentiment {
			slog.Warn("Circuit breaker open for sentiment read, returning neutral value",
				"args", cmd.Args(),
			)
			// Return "0.0" as neutral sentiment
			switch c := cmd.(type) {
			case *goredis.Cmd:
				c.SetVal("0.0")
				return nil
			}
		}
		return fmt.Errorf("redis circuit breaker open: %w", gobreaker.ErrOpenState)

	case "hset", "set", "fcall":
		// Write operations cannot be served from cache
		// These will fail, but that's expected behavior
		slog.Warn("Circuit breaker open for write operation",
			"command", cmdName,
			"args", cmd.Args(),
		)
		return fmt.Errorf("redis circuit breaker open: %w", gobreaker.ErrOpenState)

	default:
		// Unknown command - fail fast
		return fmt.Errorf("redis circuit breaker open: %w", gobreaker.ErrOpenState)
	}
}

// cacheResult stores successful read results for future fallback
func (h *CircuitBreakerHook) cacheResult(cmd goredis.Cmder) {
	cmdName := cmd.Name()

	// Only cache read operations
	switch cmdName {
	case "hget", "get":
		if err := cmd.Err(); err != nil && err != goredis.Nil {
			return
		}

		// Extract the key and value
		args := cmd.Args()
		if len(args) < 2 {
			return
		}

		key := fmt.Sprintf("%v", args[1])
		value := ""

		switch c := cmd.(type) {
		case *goredis.StringCmd:
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
func (h *CircuitBreakerHook) GetState() gobreaker.State {
	return h.cb.State()
}

// GetCounts returns the current counts (for testing/monitoring)
func (h *CircuitBreakerHook) GetCounts() gobreaker.Counts {
	return h.cb.Counts()
}
