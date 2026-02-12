# Config Caching Architecture

## Overview

The config cache is an in-memory TTL-based cache that reduces Redis config reads by 99%+ for active sessions. It sits between the sentiment engine and Redis, transparently caching `ConfigSnapshot` objects.

**Performance Impact:**
- **Before**: 20,000 Redis config reads/sec (1,000 sessions × 20 ticks/sec)
- **After**: ~100 Redis config reads/sec (cache misses + expirations)
- **Reduction**: 99.5% fewer Redis operations

## Architecture

```
┌─────────────┐
│ Broadcaster │  50ms tick loop
└──────┬──────┘
       │
       │ GetCurrentValue(sessionUUID)
       ▼
┌──────────────┐
│    Engine    │
└──────┬───────┘
       │
       │ GetSessionConfig(sessionUUID)
       ▼
┌──────────────────┐
│  ConfigCache     │  ◄─── In-Memory Cache (10s TTL)
│  - Get()         │
│  - Set()         │
│  - Invalidate()  │
└──────┬───────────┘
       │
       │ (on cache miss)
       ▼
┌──────────────────┐
│ SessionRepo      │
│ (Redis)          │
└──────────────────┘
```

## Implementation

### ConfigCache Structure

**File:** `internal/sentiment/config_cache.go`

```go
type ConfigCache struct {
    mu      sync.RWMutex                       // Read-write lock for thread safety
    entries map[uuid.UUID]*cacheEntry          // Session UUID → cache entry
    ttl     time.Duration                      // Time-to-live (10s default)
    clock   clockwork.Clock                    // Injected clock for testing
}

type cacheEntry struct {
    config    domain.ConfigSnapshot
    expiresAt time.Time
}
```

### Key Methods

| Method | Description | Complexity |
|--------|-------------|-----------|
| `Get(sessionUUID)` | Read from cache, check expiry | O(1) |
| `Set(sessionUUID, config)` | Write to cache with TTL | O(1) |
| `Invalidate(sessionUUID)` | Explicit cache removal | O(1) |
| `EvictExpired()` | Scan and remove expired entries | O(n) |
| `Clear()` | Remove all entries | O(1) |
| `Size()` | Count cached entries | O(1) |

## Cache Flow

### 1. Cache Hit (99%+ of requests)

```
Engine.GetCurrentValue()
  → ConfigCache.Get(sessionUUID)
    → Check map[sessionUUID]
    → Check expiresAt > now
    → Return cached ConfigSnapshot
  → SentimentStore.GetSentiment() (Redis Function)
  → Return decayed value
```

**Latency:** ~40 ns/op (benchmark: `BenchmarkEngine_GetCurrentValue_CacheHit`)

### 2. Cache Miss (<1% of requests)

```
Engine.GetCurrentValue()
  → ConfigCache.Get(sessionUUID)
    → Miss (not in map OR expired)
    → metrics.ConfigCacheMisses.Inc()
  → SessionRepo.GetSessionConfig() (Redis HGET)
  → ConfigCache.Set(sessionUUID, config)
  → SentimentStore.GetSentiment() (Redis Function)
  → Return decayed value
```

**Latency:** ~530 ns/op (benchmark: `BenchmarkEngine_GetCurrentValue_CacheMiss`)

### 3. Cache Invalidation (config update)

```
App.SaveConfig()
  → ConfigRepo.Update() (PostgreSQL)
  → SessionRepo.UpdateConfig() (Redis HSET)
  → Engine.InvalidateConfigCache(sessionUUID)
    → ConfigCache.Invalidate(sessionUUID)
      → delete(entries, sessionUUID)
  → Next GetCurrentValue() will miss and refetch
```

## TTL Strategy

**TTL Value:** 10 seconds

**Rationale:**
- Broadcaster ticks every 50ms (20 times/sec)
- 10s TTL covers 200 ticks
- First tick: cache miss (1 Redis read)
- Next 199 ticks: cache hits (0 Redis reads)
- Hit rate: 199/200 = **99.5%**

**TTL Trade-offs:**

| TTL | Hit Rate | Config Lag | Memory |
|-----|----------|------------|--------|
| 5s  | 99.0% | 5s max | Low |
| 10s | 99.5% | 10s max | Medium |
| 30s | 99.8% | 30s max | High |

**Decision:** 10s balances hit rate (99.5%) with acceptable config update lag.

## Eviction

### Automatic Eviction (Background Timer)

```go
// In cmd/server/main.go
configCache := sentiment.NewConfigCache(10*time.Second, clock)
stopEviction := configCache.StartEvictionTimer(1 * time.Minute)
defer stopEviction()
```

**Eviction Frequency:** 1 minute (configurable)

**Process:**
1. `EvictExpired()` iterates all entries
2. Compares `expiresAt` to current time
3. Deletes expired entries
4. Updates `ConfigCacheSize` metric

**Complexity:** O(n) where n = cached sessions

### Lazy Eviction (On Get)

Expired entries are also detected during `Get()`:
```go
if clock.Now().After(entry.expiresAt) {
    return nil, false  // Cache miss
}
```

Entries remain in map until next `EvictExpired()` call, but are treated as misses.

## Thread Safety

**Concurrency Model:** Read-Write Mutex

- **Reads** (`Get`, `Size`): Acquire `RLock()` — multiple concurrent readers allowed
- **Writes** (`Set`, `Invalidate`, `Clear`, `EvictExpired`): Acquire `Lock()` — exclusive access

**Contention Handling:**
- Broadcaster ticks (reads) never block each other
- Config updates (writes) are rare, brief critical section
- Benchmark: `BenchmarkConfigCache_ConcurrentReads` shows 166 ns/op under parallel load

## Memory Usage

### Per-Entry Overhead

**ConfigSnapshot Structure:**
```go
type ConfigSnapshot struct {
    ForTrigger     string   // ~10 bytes
    AgainstTrigger string   // ~10 bytes
    LeftLabel      string   // ~10 bytes
    RightLabel     string   // ~10 bytes
    DecaySpeed     float64  // 8 bytes
}
```

**Cache Entry:**
```go
type cacheEntry struct {
    config    ConfigSnapshot  // ~50 bytes
    expiresAt time.Time       // 24 bytes (3x int64)
}
```

**Map Overhead:** ~48 bytes per entry (Go map implementation)

**Total per entry:** ~122 bytes

### Capacity Planning

| Sessions | Memory | Redis Reduction |
|----------|--------|-----------------|
| 100 | ~18 KB | 2,000 → 20 req/sec |
| 1,000 | ~221 KB | 20,000 → 200 req/sec |
| 10,000 | ~2 MB | 200,000 → 2,000 req/sec |

**Benchmarks:** (from `BenchmarkConfigCache_MemoryUsage`)
- 100 sessions: 18 KB, 212 allocs
- 1,000 sessions: 221 KB, 2,023 allocs
- 10,000 sessions: 2 MB, 20,082 allocs

**Scaling:** Linear growth, negligible memory impact even at 10K sessions.

## Metrics

**Prometheus Metrics** (defined in `internal/metrics/metrics.go`):

```go
ConfigCacheHits      prometheus.Counter  // Successful cache lookups
ConfigCacheMisses    prometheus.Counter  // Cache misses (first load or expired)
ConfigCacheSize      prometheus.Gauge    // Current number of cached sessions
ConfigCacheEvictions prometheus.Counter  // Expired entries removed
```

**Monitoring Query Examples:**

```promql
# Cache hit rate (should be >99%)
rate(config_cache_hits_total[1m]) /
  (rate(config_cache_hits_total[1m]) + rate(config_cache_misses_total[1m]))

# Eviction rate (sessions/minute)
rate(config_cache_evictions_total[1m])

# Memory estimate (sessions × 122 bytes)
config_cache_size * 122
```

## Testing Strategy

### Unit Tests (12 tests)

**File:** `internal/sentiment/config_cache_test.go`

- Cache hit/miss behavior
- TTL expiration (using fake clock)
- Explicit invalidation
- Eviction timer
- Thread safety (`go test -race`)
- Edge cases (zero TTL, empty cache, updates)

### Integration Tests (5 tests)

**File:** `internal/sentiment/config_cache_integration_test.go`

- **High hit rate test:** 10,000 calls → 1 Redis read = **99.99% hit rate**
- **TTL expiry test:** Verifies refresh after expiration
- **Invalidation test:** Config update triggers cache clear
- **Multi-session isolation:** Per-session cache keys
- **Concurrent access:** 10 goroutines, 99% hit rate

### Benchmarks (9 benchmarks)

**File:** `internal/sentiment/config_cache_bench_test.go`

| Benchmark | Result | Insight |
|-----------|--------|---------|
| `Get` | 25 ns/op, 0 allocs | Fast read path |
| `Set` | 57 ns/op, 1 alloc | Minimal write overhead |
| `CacheHit` | 39 ns/op, 0 allocs | **13× faster than miss** |
| `CacheMiss` | 530 ns/op, 2 allocs | Includes mock Redis call |
| `ConcurrentReads` | 167 ns/op | Scales well under load |
| `MixedReadWrite` | 69 ns/op | 90% read workload |

## Design Decisions

### Why In-Memory Cache?

**Alternatives Considered:**

1. **Redis Cache (L1 + L2):** More complexity, network overhead
2. **No Cache:** 20K Redis ops/sec unsustainable
3. **Longer TTL:** Increases config update lag

**Decision:** In-memory cache with 10s TTL balances simplicity, performance, and freshness.

### Why TTL-Based Eviction?

**Alternatives Considered:**

1. **LRU Cache:** More complex, harder to predict behavior
2. **Manual Invalidation Only:** Requires perfect invalidation coverage
3. **No Eviction:** Memory leak on session churn

**Decision:** TTL-based eviction is simple, predictable, and self-healing.

### Why ConfigSnapshot (Value Type)?

**Alternatives Considered:**

1. **Pointer:** Saves memory, but risks mutation bugs
2. **Interface:** Adds indirection

**Decision:** Copy-on-read prevents accidental mutation, ensures cache safety.

## Future Enhancements

### 1. Adaptive TTL

Adjust TTL based on config update frequency:
```go
if updateRate < 1/hour {
    ttl = 30s  // Longer TTL for stable configs
} else {
    ttl = 5s   // Shorter TTL for frequently updated configs
}
```

### 2. Batch Eviction

Optimize eviction for large caches:
```go
func (c *ConfigCache) EvictExpiredBatch(batchSize int) int {
    // Process entries in chunks to reduce lock contention
}
```

### 3. Warmup on Startup

Pre-populate cache on server start:
```go
func (c *ConfigCache) Warmup(sessions []uuid.UUID) error {
    // Fetch configs for active sessions and populate cache
}
```

## Related Documentation

- **Circuit Breaker:** `docs/architecture/circuit-breaker.md` (fallback cache for Redis failures)
- **Redis Architecture:** `CLAUDE.md` (session state, Redis Functions)
- **Metrics:** `internal/metrics/metrics.go` (Prometheus instrumentation)

## References

- **Epic:** `twitch-tow-4c4` - Local Config Cache
- **Implementation:** `internal/sentiment/config_cache.go` (160 lines)
- **Tests:** `config_cache_test.go` (323 lines), `config_cache_integration_test.go` (340 lines)
- **Benchmarks:** `config_cache_bench_test.go` (253 lines)
