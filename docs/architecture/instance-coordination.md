# Instance Coordination Architecture

## Overview

ChatPulse uses Redis-based coordination mechanisms to enable fleet visibility, real-time config updates, and leader election for singleton tasks across multiple instances.

## Three Coordination Mechanisms

### 1. Instance Registry

**Purpose:** Track active instances in the fleet.

**Implementation:** Each instance sends periodic heartbeats (every 30s) to a shared Redis hash (`instances`). Instances without heartbeat for >60s are considered inactive.

**Schema:**
```
KEY: instances
TYPE: hash
FIELDS:
  <instance_id>: {"instance_id": "...", "timestamp": 1234567890, "version": "abc123"}
```

**Usage:**
```go
registry := coordination.NewInstanceRegistry(redisClient, instanceID, 30*time.Second, gitCommit)
go registry.Start(ctx)

// Query active instances
active, _ := registry.GetActiveInstances(ctx)
fmt.Printf("Fleet size: %d\n", len(active))
```

**Benefits:**
- Know how many instances are running
- Detect instance failures (no heartbeat for 60s)
- Foundation for leader election
- Enable fleet-wide metrics aggregation

**Metrics:**
- `instance_registry_size` — Number of active instances (updated every 30s)

### 2. Config Invalidation Pub/Sub

**Purpose:** Broadcast config changes to all instances for immediate cache invalidation.

**Implementation:** When a user saves config changes, the instance handling the request:
1. Updates config in PostgreSQL
2. Invalidates local config cache
3. Publishes invalidation message to Redis channel `config:invalidate`

All other instances subscribe to the channel and invalidate their local caches when they receive the message.

**Schema:**
```
CHANNEL: config:invalidate
PAYLOAD: <overlay_uuid> (UUID string)
```

**Publisher (in `app.Service.SaveConfig`):**
```go
// Invalidate local cache
s.engine.InvalidateConfigCache(overlayUUID)

// Broadcast to other instances
coordination.PublishConfigInvalidation(ctx, s.redis, overlayUUID)
```

**Subscriber (in `cmd/server/main.go`):**
```go
configInvalidator := coordination.NewConfigInvalidator(redisClient, engine)
go configInvalidator.Start(ctx)
```

**Benefits:**
- Config changes propagate in <1 second (down from 10s TTL-based cache)
- Reduced Redis polling load
- Better user experience (immediate updates)

**Fallback:** If pub/sub message is lost (Redis restart), TTL-based cache (10s) ensures eventual consistency.

**Metrics:**
- `pubsub_messages_received_total{channel="config:invalidate"}` — Messages received by subscribers

### 3. Leader Election for Orphan Cleanup

**Purpose:** Ensure only one instance runs orphan cleanup at a time (avoid duplicate Twitch API calls).

**Implementation:** Uses Redis `SETNX` with TTL to elect a leader. The leader holds a key with 30s TTL and renews it every 15s. If the leader crashes, the key expires and another instance can become leader.

**Schema:**
```
KEY: cleanup:leader
VALUE: <instance_id>
TTL: 30s
```

**Acquisition (in `app.Service.startCleanupTimer`):**
```go
acquired, err := s.leaderElector.TryAcquire(ctx)
if acquired {
    s.isLeader = true
    // Run cleanup
}
```

**Renewal (every 15s):**
```go
if s.isLeader {
    if err := s.leaderElector.Renew(ctx); err != nil {
        s.isLeader = false
        // Lost leadership
    }
}
```

**Release (graceful shutdown):**
```go
if s.isLeader {
    s.leaderElector.Release(ctx)
}
```

**Benefits:**
- Single instance runs cleanup (no duplicate Twitch API calls)
- Automatic failover if leader crashes (TTL expires)
- Reduced Twitch API usage

**Metrics:**
- `cleanup_leader{instance_id}` — 1 if this instance is leader, 0 otherwise
- `cleanup_skipped_total{reason="not_leader"}` — Cleanup cycles skipped (not leader)
- `leader_election_failures_total{reason}` — Leader election failures

## Operational Implications

### Fleet Visibility

Operators can query active instances via Prometheus:
```promql
instance_registry_size
```

Or via Redis CLI:
```bash
redis-cli HGETALL instances
```

### Config Update Latency

Before: 10 seconds (TTL-based cache)
After: <1 second (pub/sub + TTL fallback)

Query config invalidation rate:
```promql
rate(pubsub_messages_received_total{channel="config:invalidate"}[5m])
```

### Orphan Cleanup Efficiency

Before: N instances × 30s interval = N cleanup runs
After: 1 instance × 30s interval = 1 cleanup run

Query cleanup leadership:
```promql
sum(cleanup_leader) by (instance_id)  # Should always be 0 or 1
```

### Leader Failover

If the cleanup leader crashes:
1. Heartbeat stops (instance removed from registry after 60s)
2. Leader key expires after 30s (no renewal)
3. Another instance acquires leadership within next cleanup interval (30s)
4. Total failover time: 30-60s

## Failure Modes

### Redis Pub/Sub Message Loss

**Scenario:** Redis restarts, pub/sub messages lost.

**Mitigation:** TTL-based cache (10s) ensures eventual consistency. Config changes take max 10s to propagate (instead of <1s).

**Detection:** Monitor `config_cache_misses_total` — spike indicates pub/sub disruption.

### Split Brain (Multiple Leaders)

**Scenario:** Network partition prevents leader renewal, new leader elected, network heals.

**Mitigation:** Lua script ensures atomic leader renewal (check-and-renew). Only one instance holds the key at a time.

**Detection:** Monitor `sum(cleanup_leader)` — should never exceed 1.

### Registry Heartbeat Failure

**Scenario:** Instance network partition, heartbeat stops.

**Mitigation:** 60s timeout removes stale instances from registry automatically.

**Detection:** Monitor `instance_registry_size` — sudden drop indicates network issues.

### Pub/Sub Subscriber Lag

**Scenario:** Subscriber goroutine blocks, messages pile up in Redis.

**Mitigation:** Redis buffers pub/sub messages in memory. If subscriber disconnects, it misses messages (falls back to TTL-based cache).

**Detection:** Monitor `pubsub_messages_received_total` — zero rate indicates subscriber failure.

## Monitoring and Alerts

### Prometheus Metrics

**Instance Registry:**
- `instance_registry_size` — Fleet size (expected: number of deployed replicas)

**Pub/Sub:**
- `pubsub_messages_received_total{channel}` — Config invalidation messages received

**Leader Election:**
- `cleanup_leader{instance_id}` — Current leader (expected: exactly 1)
- `cleanup_skipped_total{reason="not_leader"}` — Non-leader cleanup skips
- `leader_election_failures_total{reason}` — Leader election failures

### Alert Examples

```yaml
# No cleanup leader for 2 minutes
- alert: NoCleanupLeader
  expr: sum(cleanup_leader) == 0
  for: 2m

# Multiple cleanup leaders (split brain)
- alert: MultipleCleanupLeaders
  expr: sum(cleanup_leader) > 1
  for: 1m

# Registry size mismatch
- alert: RegistrySizeMismatch
  expr: abs(instance_registry_size - 3) > 0  # Expected: 3 replicas
  for: 5m

# Pub/sub subscriber down
- alert: ConfigPubsubDown
  expr: rate(pubsub_messages_received_total{channel="config:invalidate"}[10m]) == 0
  for: 10m
```

## Architecture Decisions

### Why Redis Instead of Service Mesh?

**Redis-based coordination:**
- ✅ Simple (no additional infrastructure)
- ✅ Already required for session state
- ✅ Sufficient for current scale (<1000 instances)

**Service mesh (Consul, etcd):**
- ❌ Overkill for current scale
- ❌ Additional operational complexity
- ❌ Another failure mode to manage

### Why Pub/Sub Over Redis Streams?

**Pub/Sub:**
- ✅ Simple broadcast (1:N)
- ✅ No retention needed (TTL fallback)
- ✅ Lower memory overhead

**Streams:**
- ❌ Overkill (don't need message replay)
- ❌ Requires consumer groups
- ❌ More operational complexity

### Why Not Distributed Tracing?

Distributed tracing (Jaeger, Zipkin) would help debug cross-instance flows, but:
- ❌ Out of scope for this epic (covered in observability Phase 4)
- ❌ Most use cases are single-instance (WebSocket clients stick to one instance)
- ❌ Config updates are fire-and-forget (don't need trace correlation)

## Future Enhancements

### Dynamic Leader Election (Not Implemented)

Currently, cleanup leadership is global (one leader for entire fleet). Future enhancement: shard cleanup by session prefix:

```
KEY: cleanup:leader:a-f
VALUE: instance-1

KEY: cleanup:leader:g-m
VALUE: instance-2

KEY: cleanup:leader:n-z
VALUE: instance-3
```

Benefits:
- Parallel cleanup (3× throughput)
- Lower latency (each leader scans 1/3 of sessions)

Trade-offs:
- More complex leader election logic
- Requires session count justification (>10K sessions)

### Fleet-Wide Config Broadcast (Not Implemented)

Currently, pub/sub only broadcasts config invalidation. Future enhancement: broadcast non-config changes (e.g., feature flags, rate limits).

Benefits:
- Real-time feature flag propagation
- Dynamic rate limit updates

Trade-offs:
- More pub/sub channels to manage
- Increased Redis pub/sub load

## Testing

Integration tests cover:
- 3-instance coordination scenario
- Leader election with failover
- Concurrent leader election (10 instances)
- Config invalidation propagation
- Registry heartbeat expiry

Run tests:
```bash
make test                     # All tests (includes integration)
make test-short               # Unit tests only
go test ./internal/coordination/  # Coordination tests only
```

## References

- Epic: twitch-tow-rn7 (Instance Coordination)
- ADR: (None - coordination is infrastructure, not architecture decision)
- Related: Config caching (twitch-tow-4c4), Orphan cleanup (existing)
