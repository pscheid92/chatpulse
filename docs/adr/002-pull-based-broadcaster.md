# ADR-002: Pull-based broadcaster vs Redis pub/sub

**Status:** Accepted

**Date:** 2026-02-12

## Context

The Broadcaster component is responsible for sending sentiment updates to WebSocket clients every 50ms. Because sentiment values time-decay (they trend toward neutral over time), clients must receive *current* values that reflect the elapsed time since the last vote.

With a Redis-only architecture (ADR-001), there are two fundamental approaches to getting current values:

1. **Pull-based:** Broadcaster actively reads current values from Redis on a timer
2. **Push-based:** Vote events are published to Redis pub/sub, and Broadcaster subscribes to receive updates

Key constraints:
- **Time-decay is continuous:** The sentiment value changes every millisecond (exponential decay toward 0), not just when votes arrive
- **Multiple instances may serve the same session:** No sticky sessions (ADR-001), so multiple Broadcasters might be sending updates for the same overlay UUID
- **50ms broadcast frequency:** We want smooth animation, so updates must be frequent

## Decision

**The Broadcaster uses a 50ms ticker to pull current values from Redis. No Redis pub/sub, no push-based events.**

Specifically:

- **Tick loop:** Every 50ms, the Broadcaster iterates over all active sessions and calls `FCALL_RO get_decayed_value` for each
- **Redis Function reads:** `get_decayed_value` computes the time-decayed value atomically inside Redis (reads `value` + `last_update`, applies decay formula)
- **Fan-out happens in-memory:** Once we have the current value, we broadcast JSON to all local WebSocket clients for that session (no Redis writes)
- **No caching:** We don't cache values between ticks - every tick reads from Redis

**Code structure:**
```go
// Broadcaster.run() - simplified
func (b *Broadcaster) run() {
    ticker := time.NewTicker(50 * time.Millisecond)
    for {
        select {
        case <-ticker.C:
            for sessionUUID := range b.activeClients {
                value := b.engine.GetCurrentValue(ctx, sessionUUID)  // calls FCALL_RO
                b.broadcastToSession(sessionUUID, value)
            }
        case cmd := <-b.commands:
            // handle register/unregister commands
        }
    }
}
```

## Alternatives Considered

### 1. Redis pub/sub (push-based updates)

**Description:** When a vote is applied (via webhook), publish a message to a Redis pub/sub channel. All instances subscribe and update their local clients.

**Rejected because:**
- **Still need to compute time-decay on every broadcast:** Even with pub/sub, we'd still need to read from Redis every 50ms to compute the current decayed value. The vote event only tells us "value changed 200ms ago" - not "what is the value right now."
- **Pub/sub doesn't scale well:** All instances receive all vote messages, even if they're not serving that session. With 1000 streamers and 10 instances, each instance processes 10x the necessary messages.
- **More complex code:** Now we have two code paths: (1) tick loop to compute decay, (2) pub/sub handler to receive vote updates. The tick loop is unavoidable (we need it for decay), so pub/sub just adds complexity.
- **Race conditions:** What if a pub/sub message arrives *after* we've already computed the decayed value for this tick? Do we broadcast again immediately, or wait for next tick? Either choice adds complexity.

**Why we thought this would help:**
- Initial intuition: "Push is more efficient than pull, right?"
- Reality: Time-decay means we need continuous reads anyway, so push doesn't eliminate the tick loop.

### 2. In-memory cache with pub/sub invalidation

**Description:** Cache the last known value in memory. On each tick, use the cached value + elapsed time to compute decay locally. Invalidate the cache when a pub/sub vote message arrives.

**Rejected because:**
- **Staleness for time-decay:** If we cache `{value: 50, last_update: T}` and compute decay locally, we need to know the *exact* Redis system time to match the decay calculation in Redis Functions. Clock skew between instances would cause different instances to show different values.
- **Cache invalidation across instances is complex:** We'd need pub/sub to broadcast invalidations, plus logic to handle "what if invalidation arrives while we're mid-tick?"
- **Doesn't eliminate Redis calls:** We still need to read from Redis when cache is invalidated. For high-vote-rate streams, cache hit rate could be low.
- **More failure modes:** What if pub/sub connection drops? Do we fall back to pulling? Now we have two code paths to maintain.

### 3. Server-Sent Events (SSE) instead of WebSockets

**Description:** Use HTTP streaming (SSE) instead of WebSockets for overlay updates.

**Rejected because:**
- **Doesn't affect Redis architecture:** This is purely a transport change. We'd still need the same pull-based or push-based logic.
- **WebSocket already works:** No strong reason to change. WebSockets provide bidirectional communication (useful for future features like client-side events).
- **Browser support:** SSE has worse support in some browsers compared to WebSockets.

**Not actually an alternative to pull vs push** - just a different delivery mechanism.

## Consequences

### ✅ Positive

- **Simple code:** Single tick loop, no event handlers, no pub/sub connection management. The entire Broadcaster is ~200 lines including actor command handling.
- **Always shows current time-decayed value:** Every tick computes fresh decay. No risk of showing stale cached values or missing decay updates.
- **Easy to reason about:** Linear flow: timer fires → read from Redis → broadcast to clients. No event ordering concerns, no race conditions.
- **No pub/sub scaling issues:** Each instance only reads sessions it's actively serving (O(active_sessions_per_instance)), not all sessions in the system (O(total_sessions)).
- **Consistent behavior across instances:** All instances compute decay using the same Redis Function logic. No clock skew issues.

### ❌ Negative

- **N Redis calls per tick:** If an instance serves 100 active sessions, that's 100 Redis calls every 50ms (2000 calls/sec). This is manageable for Redis but not free.
- **50ms staleness for vote updates:** When a vote arrives, clients don't see the update until the next tick (up to 50ms later). For a real-time sentiment bar, this is acceptable, but not instant.
- **Constant Redis traffic even when no votes:** Even if no one is chatting, we're still polling Redis every 50ms. This is "wasted" traffic compared to event-driven approaches.

### 🔄 Trade-offs

- **Chose simplicity over real-time updates:** We accept 50ms staleness (vs instant pub/sub) because the sentiment bar is not a precision instrument. Users won't notice 50ms delay in the animation.
- **Accept higher Redis call frequency for simpler code:** We could reduce Redis calls by caching + invalidating, but that would make the code much more complex. We chose code simplicity over optimization.
- **Prioritize correctness (always fresh decay) over performance (cached values):** Time-decay is a core feature. We never want to show stale values. The cost is more Redis calls, but the benefit is zero staleness bugs.

## Related Decisions

- **ADR-001: Redis-only architecture** - Consequence: all reads must hit Redis (no local state). This decision determines that we need Redis calls on every tick.
- **ADR-011: Actor pattern for broadcaster** (future) - Consequence: tick processing is serial (one session at a time per instance). Could parallelize Redis reads in the future if needed.
- **ADR-004: Single bot account architecture** (future) - Context: votes arrive via webhooks, not through the Broadcaster. Broadcaster is purely read-side.

## Future Considerations

If Redis call frequency becomes a bottleneck (e.g., 10,000 active sessions per instance), we could optimize by:

1. **Batch reads:** Use Redis pipelining to send all `FCALL_RO` calls in one roundtrip
2. **Adaptive tick rate:** Reduce tick frequency for sessions with low vote rate (detected by tracking `last_update`)
3. **Read-only Redis replicas:** Route `FCALL_RO` calls to read replicas to reduce load on primary

However, these optimizations add complexity. We'll wait for profiling data before optimizing.
