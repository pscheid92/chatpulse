# Orphan Cleanup Optimization

## Overview

This document describes the optimization of ChatPulse's orphan cleanup system from O(N) keyspace scanning to O(log M + K) sorted set range queries using Redis sorted sets.

## Problem Statement

### Original Implementation (SCAN-based)

The original `ListOrphans()` implementation used Redis `SCAN` to iterate through all session keys:

```go
// Old approach: SCAN session:* pattern
for {
    keys, cursor, err := rdb.Scan(ctx, cursor, "session:*", 100).Result()
    // Check each key's last_disconnect timestamp
}
```

**Complexity**: O(N) where N = total keys in Redis (not just sessions)

**Performance impact**:
- 10K sessions: ~100ms (negligible)
- 100K sessions: 1-2 seconds (noticeable)
- 1M total keys: 10+ seconds (expensive)

**Problems**:
- Scans entire keyspace, not just sessions
- Pattern matching on every key
- No way to filter by timestamp efficiently
- Added 1000-key-per-run limit to prevent blocking Redis

### Solution: Redis Sorted Set Index

Use a sorted set to index disconnected sessions by timestamp:

```
disconnected_sessions (zset)
├─ score: Unix timestamp (disconnect time)
└─ member: session UUID
```

**Complexity**: O(log M + K) where:
- M = number of disconnected sessions
- K = number of orphans found

**Performance improvement**: 10x-100x faster at scale

## Architecture

### Redis Key Schema

**Before**:
```
session:{uuid} (hash)
  ├─ value
  ├─ broadcaster_user_id
  ├─ config_json
  ├─ last_disconnect (timestamp)
  └─ last_update
```

**After** (adds sorted set index):
```
session:{uuid} (hash) - unchanged

disconnected_sessions (zset) - NEW
  ├─ score: Unix timestamp (seconds)
  └─ member: session UUID string
```

### State Transitions

Sessions move through these states with corresponding sorted set operations:

```
[Active] ──disconnect──> [Disconnected] ──grace period──> [Orphan] ──cleanup──> [Deleted]
           (ZADD)         (in zset)         (ZRANGEBYSCORE)         (ZREM)

[Disconnected] ──resume──> [Active]
                (ZREM)
```

### Atomic Operations

All sorted set operations are performed atomically using Redis pipelines:

**MarkDisconnected** (session disconnects):
```redis
HSET session:{uuid} last_disconnect {timestamp}
ZADD disconnected_sessions {timestamp} {uuid}
```

**ResumeSession** (session reconnects):
```redis
HSET session:{uuid} last_disconnect 0
ZREM disconnected_sessions {uuid}
```

**ActivateSession** (existing session resumes):
```redis
HSET session:{uuid} last_disconnect 0
ZREM disconnected_sessions {uuid}
```

**DeleteSession** (orphan cleanup):
```redis
DEL session:{uuid}
DEL ref_count:{uuid}
DEL broadcaster:{broadcaster_id}
ZREM disconnected_sessions {uuid}
```

**ListOrphans** (find orphans):
```redis
ZRANGEBYSCORE disconnected_sessions -inf {cutoff_timestamp}
```

## Implementation Details

### SessionRepository Interface

Added method to domain interface:

```go
type SessionRepository interface {
    // ... existing methods ...

    // DisconnectedCount returns the number of sessions in disconnected state
    DisconnectedCount(ctx context.Context) (int64, error)
}
```

### ListOrphans Optimization

**Before**:
```go
func (s *SessionRepo) ListOrphans(ctx, maxAge) ([]uuid.UUID, error) {
    var cursor uint64
    for {
        keys, nextCursor, err := s.rdb.Scan(ctx, cursor, "session:*", 100).Result()
        // O(N) iteration + timestamp check per key
    }
}
```

**After**:
```go
func (s *SessionRepo) ListOrphans(ctx, maxAge) ([]uuid.UUID, error) {
    cutoff := s.clock.Now().Add(-maxAge).Unix()

    // O(log M + K) range query
    members, err := s.rdb.ZRangeByScore(ctx, "disconnected_sessions", &redis.ZRangeBy{
        Min: "-inf",
        Max: fmt.Sprintf("%d", cutoff),
    }).Result()

    // Parse UUIDs
    return parseUUIDs(members)
}
```

### Metrics

New metric tracks sorted set size:

```go
DisconnectedSessionsCount = promauto.NewGauge(
    prometheus.GaugeOpts{
        Name: "disconnected_sessions_count",
        Help: "Number of sessions in disconnected state (sorted set size)",
    },
)
```

Updated in `CleanupOrphans()`:
```go
func (s *Service) CleanupOrphans(ctx) error {
    // Update metric
    if count, err := s.store.DisconnectedCount(ctx); err == nil {
        metrics.DisconnectedSessionsCount.Set(float64(count))
    }
    // ... rest of cleanup
}
```

## Migration

### Migration Script

Provided as `cmd/migrate-orphan-cleanup/main.go`:

```go
go run cmd/migrate-orphan-cleanup/main.go --redis="redis://localhost:6379"
```

**Features**:
- Dry-run mode (`--dry-run`)
- Verbose logging (`--verbose`)
- Idempotent (ZADD is idempotent)
- Skips active sessions (last_disconnect = 0)
- Verifies sorted set size after migration

**Algorithm**:
1. SCAN for all `session:*` keys
2. HGET each key's `last_disconnect` field
3. Skip if `last_disconnect = 0` (active session)
4. ZADD to `disconnected_sessions` with timestamp as score
5. Verify ZCARD matches expected count

### Migration Steps

**1. Deploy new code** (sorted set operations added, SCAN still used):
```bash
git pull origin main
make build
systemctl restart chatpulse
```

**2. Run migration script**:
```bash
# Dry run first
go run cmd/migrate-orphan-cleanup/main.go --dry-run --verbose

# Real migration
go run cmd/migrate-orphan-cleanup/main.go
```

**3. Monitor sorted set metric**:
```promql
disconnected_sessions_count
```

**4. Validate correctness** (both methods should return same results):
```bash
# Compare SCAN vs ZSET results during overlap period
```

**5. Verification**:
- Check sorted set size matches disconnected session count
- Monitor for missed orphans (none should occur)
- Watch cleanup duration (should drop significantly)

### Rollback Plan

If issues occur:

1. Revert code deployment
2. Old SCAN-based implementation resumes
3. Sorted set remains in Redis (benign, ignored by old code)
4. Can retry migration after fixing issues

## Performance Benchmarks

### Expected Improvements

| Sessions | SCAN (old) | ZSET (new) | Speedup |
|----------|------------|------------|---------|
| 1K       | 10ms       | <1ms       | 10x     |
| 10K      | 100ms      | 2ms        | 50x     |
| 100K     | 1-2s       | 10ms       | 100x+   |
| 1M keys  | 10s+       | 20ms       | 500x+   |

### Integration Test Benchmarks

Run benchmarks:
```bash
go test -bench=BenchmarkListOrphans -benchtime=10s ./internal/redis
```

Example output:
```
BenchmarkListOrphans_SortedSet/size=100-8    5000    250 µs/op
BenchmarkListOrphans_SortedSet/size=1000-8   2000    850 µs/op
BenchmarkListOrphans_SortedSet/size=10000-8   500   2500 µs/op
```

## Monitoring

### Metrics to Watch

**1. Sorted Set Size**:
```promql
disconnected_sessions_count
```
Alert if anomalous (sudden spike indicates leak or issue).

**2. Cleanup Duration**:
```promql
histogram_quantile(0.95, rate(orphan_cleanup_duration_seconds_bucket[5m]))
```
Should drop significantly after migration (10x-100x improvement).

**3. Orphans Deleted**:
```promql
rate(orphan_sessions_deleted_total[5m])
```
Should remain stable (correctness validation).

**4. Keys Scanned** (deprecated after migration):
```promql
rate(orphan_cleanup_keys_scanned_total[5m])
```
Should drop to zero after switchover.

### Alerting Rules

```yaml
# Sorted set size anomaly
- alert: DisconnectedSessionsSpiking
  expr: disconnected_sessions_count > 10000
  for: 5m
  annotations:
    summary: Unusually high disconnected sessions count

# Cleanup too slow (after optimization)
- alert: OrphanCleanupSlow
  expr: histogram_quantile(0.95, rate(orphan_cleanup_duration_seconds_bucket[5m])) > 0.1
  for: 5m
  annotations:
    summary: Orphan cleanup taking >100ms (expected <10ms with sorted set)
```

## Correctness Guarantees

### Idempotency

All operations are idempotent:
- **ZADD**: Adding same member multiple times updates score (safe)
- **ZREM**: Removing non-existent member is no-op (safe)
- **ZRANGEBYSCORE**: Read-only query (safe)

### Atomicity

Pipelines ensure atomic state transitions:
- Session hash and sorted set updated together
- No partial state (either both succeed or both fail)

### Race Condition Handling

**Scenario**: Session reconnects during cleanup scan

**Before** (SCAN-based):
- SCAN finds session
- Check `last_disconnect` timestamp
- Delete if old enough
- **Race**: Session might reconnect after timestamp check but before delete

**After** (ZSET-based):
- ZRANGEBYSCORE finds session
- DeleteSession checks `ref_count`
- Returns `ErrSessionActive` if `ref_count > 0`
- Cleanup skips deletion
- **Protection**: Same race handling, but faster query reduces window

### Reconciliation

If sorted set gets out of sync:

**Periodic reconciliation job** (optional):
```go
// Scan all sessions and rebuild sorted set
func ReconcileSortedSet(ctx) error {
    // SCAN session:*
    // Check last_disconnect
    // ZADD to new temporary sorted set
    // RENAME to replace old set
}
```

Run weekly or on-demand if metric anomalies detected.

## Testing

### Unit Tests

Location: `internal/redis/session_repository_orphan_test.go`

Coverage:
- MarkDisconnected adds to sorted set
- ResumeSession removes from sorted set
- ListOrphans returns correct results
- DisconnectedCount returns correct size
- Invalid UUIDs are skipped
- Error handling (Redis failures)

### Integration Tests

Location: `internal/redis/session_repository_orphan_integration_test.go`

Coverage:
- Full lifecycle (activate → disconnect → resume → delete)
- Grace period filtering
- Multiple timestamps (range query correctness)
- Sorted set integrity (no orphaned entries)
- Performance benchmarks (1K, 10K sessions)

Run tests:
```bash
# Unit tests only (fast)
go test -short ./internal/redis

# Integration tests (requires Docker)
go test ./internal/redis

# Benchmarks
go test -bench=. -benchtime=10s ./internal/redis
```

## Risks & Mitigation

### Risk: Sorted Set Out of Sync

**Scenario**: Bug causes sorted set to miss some disconnected sessions

**Indicators**:
- `disconnected_sessions_count` metric too low
- Orphans not being cleaned up
- Memory leak (sessions never deleted)

**Mitigation**:
1. Periodic reconciliation job (weekly)
2. Monitor metric for anomalies
3. Manual reconciliation if detected:
   ```bash
   go run cmd/migrate-orphan-cleanup/main.go
   ```

### Risk: Sorted Set Too Large

**Scenario**: Many sessions disconnect, sorted set grows unbounded

**Indicators**:
- `disconnected_sessions_count` metric growing
- ZRANGEBYSCORE becomes slower

**Mitigation**:
1. Sorted set auto-expires: Orphans are deleted every 30s
2. If cleanup falls behind, trigger manual cleanup
3. Adjust cleanup frequency if needed (increase from 30s default)

### Risk: Migration Failures

**Scenario**: Migration script fails partway through

**Mitigation**:
1. Migration is idempotent (can re-run safely)
2. Dry-run mode validates before real run
3. Old SCAN-based code continues working
4. No data loss (sorted set is additive, doesn't remove anything)

## Future Enhancements

### 1. TTL on Sorted Set Members

Redis doesn't support per-member TTL in sorted sets. Alternative:

```redis
# Add TTL field to session hash
ZADD disconnected_sessions {timestamp} {uuid}
EXPIRE session:{uuid} {24h}
```

On cleanup, validate session exists before processing.

### 2. Batch Deletions

Currently deletes orphans one-by-one. Optimize with Lua script:

```lua
-- batch_delete_orphans.lua
local orphans = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
for _, uuid in ipairs(orphans) do
    redis.call('DEL', 'session:' .. uuid)
    redis.call('ZREM', KEYS[1], uuid)
end
return #orphans
```

### 3. Automatic Reconciliation

Add periodic reconciliation (weekly):

```go
// In app.Service
func (s *Service) StartReconciliation() {
    ticker := time.NewTicker(7 * 24 * time.Hour)
    go func() {
        for range ticker.C {
            s.ReconcileSortedSet(context.Background())
        }
    }()
}
```

## References

- [Redis Sorted Sets Documentation](https://redis.io/docs/data-types/sorted-sets/)
- [ZRANGEBYSCORE Command](https://redis.io/commands/zrangebyscore/)
- [Redis Pipelining](https://redis.io/docs/manual/pipelining/)
- [ADR-001: Redis-only Architecture](../adr/001-redis-only-architecture.md)
