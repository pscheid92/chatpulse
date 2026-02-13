# Redis Circuit Breaker Architecture

## Overview

The Redis circuit breaker provides graceful degradation when Redis becomes unavailable or slow. It prevents cascading failures by failing fast when Redis is unhealthy and serving stale data where acceptable.

## Architecture Decision

We implemented the circuit breaker using a **hooks-based approach** rather than wrapping the Redis client.

**Rationale:**
- Leverages existing `redis.Hook` infrastructure (consistent with `MetricsHook`)
- Single implementation point covers all Redis operations automatically
- Cleaner, more maintainable architecture
- No need to wrap 50+ Redis methods

## Circuit Breaker States

```
┌──────────┐
│  CLOSED  │────┐ Normal operation
└──────────┘    │ (requests pass through)
      │         │
      │ 5 failures @ 60% rate
      │         │  (within 10s window)
      ▼         │
┌──────────┐    │
│   OPEN   │    │ Fail fast
└──────────┘    │ (no Redis calls)
      │         │
      │ 30s delay
      │         │
      ▼         │
┌──────────┐    │
│ HALF-OPEN│    │ Testing recovery
└──────────┘    │ (1 test request)
      │         │
      │ 1 success
      └─────────┘
```

### State Transitions

**CLOSED → OPEN:**
- Trigger: 5+ requests with ≥60% failure rate within 10-second rolling window
- Behavior: Circuit opens, all requests fail fast without calling Redis

**OPEN → HALF-OPEN:**
- Trigger: 30 seconds elapsed since circuit opened
- Behavior: Allow 1 test request to check if Redis recovered

**HALF-OPEN → CLOSED:**
- Trigger: 1 successful request
- Behavior: Circuit closes, normal operation resumes

**HALF-OPEN → OPEN:**
- Trigger: Any failure during half-open state
- Behavior: Circuit re-opens for another 30-second delay

## Fallback Behavior

### Read Operations (GET, HGET)
- **Circuit Closed:** Normal Redis operation
- **Circuit Open:** 
  - Serve from cache if available and not expired (5-minute TTL)
  - Return error if cache miss or expired

### Read-Only Functions (FCALL_RO)
- **Circuit Closed:** Normal Redis operation
- **Circuit Open:**
  - Sentiment reads (`get_decayed_value`): Return neutral value (0.0)
  - Other functions: Return error

### Write Operations (SET, HSET, FCALL)
- **Circuit Closed:** Normal Redis operation
- **Circuit Open:** Fail immediately with circuit breaker error
- **Rationale:** Writes cannot be safely cached or faked

### Pipeline Operations
- **Circuit Closed:** Normal Redis operation
- **Circuit Open:** Fail immediately (no good fallback for atomic operations)

## Cache Implementation

### Cache Storage
- In-memory `sync.Map` with TTL tracking
- Stores successful read results for fallback
- **Cache Entry:** `{data: string, timestamp: time.Time}`

### Cache Behavior
- **Write:** On successful GET/HGET, cache the value with current timestamp
- **Read:** When circuit open, check cache and validate TTL (5 minutes)
- **Expiry:** Entries older than 5 minutes are not served

### Cache Limitations
- No LRU eviction (future enhancement if memory becomes concern)
- 5-minute TTL prevents unbounded growth
- Cache is local to each instance (not shared across pods)

## Configuration

```go
circuitbreaker.NewBuilder[any]().
    WithFailureRateThreshold(0.6, 5, 10*time.Second). // 60% failure rate, min 5 requests, 10s rolling window
    WithDelay(30 * time.Second).                       // Wait 30s before transitioning to half-open
    WithSuccessThreshold(1).                           // 1 success in half-open to close
    OnStateChanged(func(e circuitbreaker.StateChangedEvent) { ... }).
    Build()
```

### Tuning Parameters

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| **Failure Rate Threshold** | 60% with min 5 requests in 10s | Avoids false positives from transient failures |
| **Delay** | 30 seconds | Gives Redis time to recover before test requests |
| **Success Threshold** | 1 | Conservative: single success proves recovery |
| **Cache TTL** | 5 minutes | Stale data acceptable for short period during outages |

## Metrics

### Circuit Breaker State
```promql
# Current state (0=closed, 1=half-open, 2=open)
circuit_breaker_state{component="redis"}

# State transitions
rate(circuit_breaker_state_changes_total{component="redis"}[5m])
```

### Alerting
```promql
# Alert when circuit open for >1 minute
circuit_breaker_state{component="redis"} == 2
```

## Monitoring

### Dashboard Queries

**Circuit breaker state over time:**
```promql
circuit_breaker_state{component="redis"}
```

**State transition rate:**
```promql
rate(circuit_breaker_state_changes_total{component="redis",state="open"}[5m])
```

**Redis operation failure rate:**
```promql
rate(redis_operations_total{status="error"}[5m]) / 
rate(redis_operations_total[5m])
```

## Testing

### Unit Tests
- `circuit_breaker_hook_test.go` - 14 test cases
- Covers all state transitions
- Validates cache behavior and TTL
- Tests command-specific fallback logic
- Verifies `redis.Nil` is not counted as a failure

### Integration Tests
- `circuit_breaker_integration_test.go` - 3 scenarios
- Uses testcontainers to start/stop real Redis
- Simulates actual outages
- Validates full lifecycle and recovery

### Running Tests
```bash
# Unit tests (fast, no Docker required)
go test -short ./internal/redis -run TestCircuitBreaker

# Integration tests (requires Docker)
go test ./internal/redis -run TestCircuitBreakerIntegration
```

## Troubleshooting

### Circuit Opens Frequently
**Symptom:** Circuit breaker opens often, even when Redis appears healthy

**Possible Causes:**
1. Network latency to Redis (operations timing out)
2. Redis under heavy load (slow responses)
3. Failure threshold too low (increase from 60% or 5 requests)

**Investigation:**
```promql
# Check Redis operation latency
histogram_quantile(0.99, redis_operation_duration_seconds)

# Check Redis error types
redis_operations_total{status="error"}
```

### False Positives
**Symptom:** Circuit opens but Redis is actually healthy

**Solution:** 
- Increase failure threshold (e.g., 70% or 10 requests)
- Increase interval (e.g., 120 seconds)

### Circuit Never Opens
**Symptom:** Redis failures don't trigger circuit breaker

**Possible Causes:**
1. Not enough requests (need ≥5 within 60s)
2. Failure rate below 60% threshold
3. Hook not properly registered

**Verification:**
```go
// Check circuit breaker is registered in client.go:
rdb.AddHook(NewCircuitBreakerHook())
```

### Stale Data Issues
**Symptom:** Application serving incorrect/old data

**Cause:** Cache TTL too long (5 minutes)

**Solution:**
- Reduce cache TTL (trade-off: less availability during outages)
- Add cache invalidation logic for critical data

## Implementation Notes

### Why Hooks Over Wrapper?
We chose the hooks pattern over wrapping the Redis client for several reasons:

1. **Consistency:** Already using `MetricsHook` - same pattern
2. **Coverage:** Hooks automatically intercept all operations
3. **Maintainability:** Single implementation vs. 50+ method wrappers
4. **Flexibility:** Can add more hooks without modifying client code

### Hook Order
```go
rdb.AddHook(NewCircuitBreakerHook())  // Innermost - first to execute
rdb.AddHook(&MetricsHook{})           // Outer - wraps circuit breaker
```

Circuit breaker hook executes first, so metrics capture both successful operations and circuit-breaker-blocked operations.

### Standalone API (TryAcquirePermit)
We use failsafe-go's **standalone API** (`TryAcquirePermit` + `RecordSuccess`/`RecordError`) rather than `failsafe.With().Run()`. This fits the Redis hooks pattern — we control execution flow via `next()` and need custom fallback logic when the circuit is open.

### redis.Nil Handling
`redis.Nil` (cache miss) is explicitly treated as a success, not a failure. This is correct — a cache miss is not a Redis infrastructure failure. The previous gobreaker implementation counted `redis.Nil` as a failure since it was a non-nil error returned from `Execute`.

### Thread Safety
- Circuit breaker state managed by `failsafe-go` library (mutex-protected, concurrency safe)
- Cache uses `sync.RWMutex` for concurrent access
- No additional locking required

## Future Enhancements

### LRU Cache Eviction
Currently, cache has no size limit. Future enhancement:
```go
type lruCache struct {
    maxSize int
    evictOldest func()
}
```

### Per-Operation Circuit Breakers
Split circuit breaker by operation type:
- Separate breaker for reads vs. writes
- More granular control over fallback

### Distributed Circuit Breaker
Share circuit breaker state across instances:
- Use Redis pub/sub to coordinate state
- Faster detection of cluster-wide issues

### Smart Cache Warming
Pre-populate cache with frequently accessed keys:
- Track hot keys
- Background refresh before expiry

## References

- [failsafe-go Library](https://github.com/failsafe-go/failsafe-go)
- [failsafe-go Circuit Breaker Docs](https://failsafe-go.dev/circuit-breaker/)
- [Redis Hooks Documentation](https://redis.uptrace.dev/guide/go-redis-hooks.html)
- [Circuit Breaker Pattern](https://martinfowler.com/bliki/CircuitBreaker.html)
