# ADR-006: Ref counting for multi-instance coordination

**Status:** Accepted

**Date:** 2026-02-12

## Context

With stateless instances (ADR-005), multiple instances may serve the same session simultaneously. For example:
- User opens overlay in two browser tabs → two WebSocket connections → might route to different instances
- User refreshes browser → disconnects from instance A, reconnects to instance B

When all clients disconnect from a session, we need to clean up the session from Redis (delete the session hash, stop Twitch EventSub subscription). But how do we coordinate cleanup across multiple instances?

**The problem:**
- Instance A serves 2 clients for session X
- Instance B serves 1 client for session X
- All clients disconnect
- How do instances A and B coordinate to ensure cleanup happens exactly once?

**Constraints:**
- No sticky sessions (ADR-005) means we can't rely on "only one instance per session"
- Distributed system, failures inevitable (instance crashes, network partitions)
- Redis is our only coordination mechanism (no ZooKeeper, etcd, or other consensus systems)

## Decision

**Use Redis INCR/DECR for ref counting. Accept eventual consistency and rely on a cleanup timer for grace period.**

Specifically:

- **Ref count key:** `ref_count:{overlayUUID}` (integer stored in Redis)
- **Increment on first client connect:** When an instance starts serving a session (first client connects), call `INCR ref_count:{uuid}`. This tells other instances: "I'm serving this session."
- **Decrement when last client disconnects:** When the last local client disconnects, call `DECR ref_count:{uuid}` + `HSET session:{uuid} last_disconnect {timestamp}`. This tells other instances: "I'm no longer serving this session."
- **Cleanup timer (30s grace period):** Every 30 seconds, scan for sessions where:
  - `ref_count = 0` (no instances serving)
  - `last_disconnect > 30 seconds ago` (grace period expired)
  - Delete session from Redis, unsubscribe from Twitch EventSub
- **Accept race conditions:** Multiple instances might decrement simultaneously, increment during cleanup, etc. The 30s timer provides eventual consistency.

**Race conditions we accept:**

1. **Two instances increment simultaneously:**
   - Instance A: `INCR ref_count:X` → 1
   - Instance B: `INCR ref_count:X` → 2
   - **Outcome:** ref_count = 2 (correct! both instances serving)

2. **Decrement + delete race:**
   - Instance A: `DECR ref_count:X` → 0, `HSET last_disconnect`
   - Instance B: `DECR ref_count:X` → -1 (or key already deleted by cleanup)
   - **Outcome:** Orphaned key or negative ref count. Cleanup timer finds it and deletes after 30s.

3. **Increment during cleanup scan:**
   - Cleanup timer: scans, sees `ref_count:X = 0` + `last_disconnect > 30s`, queues delete
   - Instance C: client connects, `INCR ref_count:X` → 1 (before delete executes)
   - **Outcome:** Session deleted while instance C thinks it's active. Instance C's next operation will fail (session not found), triggering re-activation.

4. **Instance crashes before decrement:**
   - Instance A: serves session X, crashes, never decrements ref count
   - **Outcome:** `ref_count:X` stuck at 1. Cleanup timer sees `last_disconnect` was never set (or set by other instance). After 30s of inactivity, cleanup removes it.

**Key insight:** We don't need perfect consistency. It's OK if sessions linger for 30 seconds. It's OK if a session is deleted and immediately recreated. The 30s grace period gives us eventual consistency without complex distributed locks.

**Code flow:**
```go
// app/service.go - WebSocket connects
s.sessions.IncrRefCount(ctx, sessionUUID)  // INCR ref_count:{uuid}

// app/service.go - Last client disconnects
s.sessions.DecrRefCount(ctx, sessionUUID)  // DECR ref_count:{uuid}
s.sessions.MarkDisconnected(ctx, sessionUUID)  // HSET last_disconnect

// app/service.go - Cleanup timer (every 30s)
orphans := s.sessions.ListOrphans(ctx, 30*time.Second)  // ref_count=0 + last_disconnect>30s
for _, uuid := range orphans {
    s.sessions.DeleteSession(ctx, uuid)  // DEL session:{uuid}, DEL ref_count:{uuid}
    s.twitch.Unsubscribe(ctx, uuid)  // Background goroutine
}
```

## Alternatives Considered

### 1. Distributed locks (Redlock algorithm)

**Description:** Use Redlock (Redis-based distributed lock) to acquire a lock before cleanup. Only the instance holding the lock can delete a session.

Redlock requires:
- 5 independent Redis nodes (not replicas)
- Clock skew detection (<1% difference)
- Retry logic with exponential backoff
- Lock timeout + auto-release

**Rejected because:**
- **Complex protocol:** Redlock requires 5 Redis nodes, majority quorum (3 of 5), and careful clock synchronization. This is a significant operational burden.
- **Overkill for best-effort cleanup:** We don't need strong consistency for cleanup. If a session is deleted twice (race condition), nothing breaks. If a session lingers for 30 seconds, that's acceptable. Redlock solves problems we don't have.
- **Performance overhead:** Acquiring a lock adds latency (5 Redis calls for lock acquisition, 5 for release). Our cleanup timer runs every 30s and might process 100+ sessions - that's 1000+ Redis calls per cleanup cycle.
- **Single point of failure:** If we can't acquire a lock (e.g., Redis partition), cleanup stops. With ref counting + 30s timer, cleanup is self-healing (orphans eventually cleaned).

**When this would make sense:**
- If cleanup had side effects that must happen exactly once (e.g., charging customer's credit card)
- If orphaned sessions caused data corruption or loss (they don't - Redis memory is cheap)

### 2. Leader election (Raft, etcd, or Redis-based)

**Description:** Elect a single "leader" instance. Only the leader runs the cleanup timer. Other instances just increment/decrement ref counts.

**Rejected because:**
- **Adds external dependency:** Using etcd or Consul for leader election means adding another service to deploy, monitor, and manage. Increases system complexity.
- **Leader becomes bottleneck:** If the leader crashes and a new leader is elected, there's a gap where no cleanup happens. With distributed cleanup (every instance runs the timer), cleanup is more resilient.
- **Doesn't eliminate race conditions:** Even with a single leader, we still have races (what if an instance increments ref count just as the leader is deleting?). Leader election doesn't solve the fundamental coordination problem.
- **Redis-based leader election has same complexity as Redlock:** To implement leader election in Redis, we'd need the same primitives as Redlock (locks with TTL, quorum, etc.).

**When this would make sense:**
- If cleanup was CPU-intensive and we wanted to offload it to a dedicated instance
- If we needed strict ordering of cleanup operations (we don't)

### 3. Sticky sessions (no coordination needed)

**Description:** Use sticky sessions (ADR-005 alternative) so only one instance ever serves a session. No ref counting needed.

**Rejected because:**
- See ADR-005 for full rationale. Summary: sticky sessions don't solve crash cleanup, cause uneven load, and contradict the Redis-only architecture goal.
- Even with sticky sessions, we'd still need cleanup coordination for crashed instances. So sticky sessions don't eliminate this problem.

## Consequences

### ✅ Positive

- **Simple implementation:** `INCR` and `DECR` are atomic Redis operations with <1ms latency. No complex protocols, no external dependencies.
- **Fast operations:** Incrementing/decrementing ref count adds negligible overhead to WebSocket connect/disconnect (already doing Redis operations for session activation).
- **No external dependencies:** Uses only Redis (already required by ADR-001). No ZooKeeper, etcd, Consul, or other coordination services.
- **Self-healing:** The 30s cleanup timer automatically fixes orphaned sessions (from ref count races, crashed instances, etc.). No manual intervention needed.
- **Resilient to failures:** If an instance crashes without decrementing, the cleanup timer eventually removes the session. No permanent leaks.

### ❌ Negative

- **30s cleanup delay:** Sessions linger in Redis for up to 30 seconds after all clients disconnect. This uses Redis memory unnecessarily (though memory is cheap - ~1KB per session).
- **Race conditions possible:** Two instances might decrement simultaneously, cleanup might run while increment is in flight, etc. These races are rare and eventually resolved by the cleanup timer, but they can cause temporary inconsistency.
- **Orphaned keys from races:** If instance A decrements to 0 and instance B decrements to -1 before the key is deleted, we have an orphaned `ref_count:{uuid}` key. The cleanup timer removes it eventually, but it exists temporarily.
- **Negative ref counts possible:** If two instances decrement simultaneously, ref count might go negative. This is harmless (cleanup timer treats negative the same as zero) but semantically weird.

### 🔄 Trade-offs

- **Chose simplicity + performance over strong consistency:** We could use Redlock or leader election for perfect consistency, but the complexity and performance overhead aren't justified. 30s eventual consistency is acceptable for our use case.
- **Accept 30s cleanup window for self-healing:** The grace period means sessions linger, but it also means the system automatically recovers from race conditions. We chose resilience over immediacy.
- **Prioritize availability (no locks) over perfect cleanup:** With distributed locks, cleanup might block if we can't acquire a lock. With ref counting, cleanup always happens (eventually). We chose "eventually clean" over "perfectly clean or blocked."

## Related Decisions

- **ADR-005: Stateless instances** - Consequence: multiple instances can serve the same session, so we need ref counting. This decision wouldn't be necessary with sticky sessions.
- **ADR-010: Eventual consistency over strong consistency** (future) - Philosophy: we accept eventual consistency throughout the system. Ref counting is one example of this philosophy.
- **ADR-001: Redis-only architecture** - Context: ref counting uses Redis primitives (`INCR`/`DECR`). Redis is our coordination mechanism.

## Implementation Notes

**Cleanup timer interval:**
The 30s grace period + 30s timer interval means orphaned sessions are cleaned 30-60 seconds after disconnect. This is acceptable. If we needed faster cleanup (e.g., 5s), we'd run the timer more frequently (every 5s) and reduce the grace period (e.g., 10s).

**Negative ref count handling:**
If `DECR` results in a negative ref count, treat it as 0 for cleanup purposes:
```go
refCount, err := redis.Get(ctx, fmt.Sprintf("ref_count:%s", uuid)).Int()
if refCount <= 0 {
    // Consider for cleanup
}
```

**Monitoring:**
Track these metrics to detect ref counting issues:
- **Orphan count per cleanup cycle:** How many sessions were cleaned up? Should be <10 typically. If >100, investigate (might indicate a bug where ref counts aren't being decremented).
- **Ref count distribution:** Histogram of ref counts across all sessions. Most should be 0 (inactive) or 1-2 (active on 1-2 instances). If many sessions have ref_count > 5, investigate (might indicate instances not decrementing).
- **Negative ref counts:** Count of sessions with negative ref count. Should be 0 always. If >0, investigate (indicates DECR race condition).

**Race condition mitigation:**
The 30s grace period is deliberately chosen to be longer than typical reconnect times (user refreshes browser, WebSocket reconnects in <5s). This reduces the probability of "delete session just as user reconnects" races.

## Future Considerations

If the 30s cleanup delay becomes problematic (e.g., "we're paying for Redis memory for idle sessions"), we could:

1. **Reduce grace period to 10s:** Requires more frequent timer (every 5s) but reduces memory usage
2. **Immediate cleanup with re-activation:** Delete session immediately on ref_count=0, accept that re-connects will trigger cold start (fetch config from PostgreSQL)
3. **Separate hot/cold tiers:** Active sessions in Redis, inactive sessions in PostgreSQL. Cleanup moves from hot to cold tier instead of deleting.

However, these optimizations add complexity. Current approach (30s grace, simple INCR/DECR) is sufficient for foreseeable scale.
