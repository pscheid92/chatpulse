# ADR-010: Graceful degradation - eventual consistency over strong consistency

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse is a distributed system with multiple components:
- **Multiple instances** running in parallel (horizontal scaling)
- **Redis** for shared state (sessions, sentiment, ref counts)
- **PostgreSQL** for durable storage (users, config)
- **Network communication** between components (latency, partitions, failures)

In distributed systems, the **CAP theorem** states we must choose 2 of 3:
- **C**onsistency: All nodes see the same data at the same time
- **A**vailability: Every request receives a response (even if stale)
- **P**artition tolerance: System continues despite network failures

Since network partitions are inevitable (P is not optional), we must choose between C and A.

**The question:** Should ChatPulse prioritize consistency (all instances show identical state, but may block on failures) or availability (always respond, but may show stale data)?

**Use case context:**
- ChatPulse displays a **sentiment bar** (not financial transactions, not medical records)
- **Stale data is acceptable:** Showing "sentiment is 50" when it's actually 52 is fine (it's a trend indicator, not a precise measurement)
- **Downtime is worse than staleness:** If the overlay goes dark during a stream, viewers notice. If the sentiment bar is 2% off, nobody cares.

## Decision

**Accept eventual consistency throughout the system. Prioritize availability over strong consistency. Design for self-healing.**

Specifically:

### Core Principles

1. **Availability over consistency:** When we must choose, show stale data instead of showing nothing.
2. **Eventual consistency over immediate consistency:** It's OK if different instances show different values for a short time (seconds), as long as they eventually converge.
3. **Self-healing over prevention:** Design systems to automatically recover from inconsistencies (cleanup timers) instead of preventing all inconsistencies (distributed locks).
4. **No distributed locks:** Avoid Redlock, etcd leader election, or other consensus protocols. Use best-effort coordination (INCR/DECR, cleanup timers).

### Design Patterns

**Pattern 1: Accept race conditions, clean up with timers**
- Don't try to prevent races (expensive: distributed locks)
- Accept that races will happen (concurrent updates, cleanup vs activation)
- Use timers to clean up artifacts (orphaned keys, negative ref counts)

**Pattern 2: Idempotent operations**
- Make operations safe to retry (activate session, subscribe to EventSub)
- Detect "already exists" as success (Twitch 409 Conflict = already subscribed = OK)

**Pattern 3: Read-optimized eventual consistency**
- Write to PostgreSQL (source of truth), update Redis opportunistically
- Reads come from Redis (fast), fall back to PostgreSQL on miss (cold start)
- Stale Redis data is acceptable (will be refreshed on next activation)

## Examples of Eventual Consistency in ChatPulse

### Example 1: Ref counting race conditions (ADR-006)

**Scenario:** Two instances decrement ref count simultaneously when last clients disconnect.

**Race condition:**
```
Instance A: DECR ref_count:X → 0
Instance B: DECR ref_count:X → -1 (or key already deleted by A)
Result: Orphaned key with negative ref count
```

**Strong consistency approach (rejected):**
- Acquire distributed lock before DECR
- Only the lock holder can decrement
- Release lock after cleanup

**Eventual consistency approach (accepted):**
- Accept the race condition (negative ref count is harmless)
- Cleanup timer runs every 30s, finds orphaned keys
- Timer deletes keys where `ref_count <= 0` and `last_disconnect > 30s ago`
- **Result:** Eventually consistent (within 30-60s)

**Why this is acceptable:**
- Orphaned keys use minimal Redis memory (~100 bytes per key)
- 30s window is short enough that memory doesn't accumulate
- System self-heals automatically (no manual intervention)

### Example 2: Config updates not live (ADR-007)

**Scenario:** Streamer updates sentiment triggers (changes "Pog" to "POGGERS"), but active sessions continue using old config.

**Race condition:**
```
Streamer: POST /dashboard/config → UPDATE configs SET for_trigger = 'POGGERS'
Active session in Redis: config_json = '{"for_trigger": "Pog", ...}'
Votes for "POGGERS" don't match until session reactivates
```

**Strong consistency approach (rejected):**
- On config save, acquire lock on all active sessions
- Update PostgreSQL, then update Redis for all sessions
- Release locks

**Eventual consistency approach (accepted):**
- Update PostgreSQL immediately (source of truth)
- Update Redis for active sessions opportunistically (if session exists)
- If session not active, do nothing (config will be fetched from PostgreSQL on next activation)
- **Result:** Eventually consistent (next activation or manual reconnect)

**Why this is acceptable:**
- Config changes are rare (streamers set triggers once, rarely change)
- Impact is minor (old trigger still works, just doesn't match new one)
- Streamer can force update by asking viewers to refresh (uncommon case)

### Example 3: Cleanup vs activation race (ADR-006)

**Scenario:** Cleanup timer scans for orphaned sessions while a new client connects and activates the session.

**Race condition:**
```
Cleanup timer: List orphaned sessions → finds session X (ref_count=0, last_disconnect=31s ago)
Instance C: Client connects → INCR ref_count:X → 1
Cleanup timer: DELETE session:X (executes after INCR)
Instance C: Reads session:X → not found (was just deleted)
```

**Strong consistency approach (rejected):**
- Cleanup timer acquires lock on session before delete
- If lock held, skip delete (someone activated it)
- Release lock

**Eventual consistency approach (accepted):**
- Accept the race condition (session deleted during activation)
- Instance C's next operation fails (session not found)
- Triggers re-activation (fetch from PostgreSQL, create in Redis)
- **Result:** Self-healing (correct end state within milliseconds)

**Why this is acceptable:**
- Rare event (cleanup cycle is 30s, activation window is <100ms)
- Self-heals immediately (re-activation is automatic)
- Correct end state (session active in Redis)

## Alternatives Considered

### 1. Strong consistency with distributed locks (Redlock)

**Description:** Use the Redlock algorithm to acquire distributed locks before critical operations (ref count decrement, cleanup, activation).

Redlock requires:
- 5 independent Redis nodes (not replicas)
- Majority quorum (acquire locks on 3+ nodes)
- Clock synchronization (<1% drift)
- Retry with exponential backoff

**Rejected because:**
- **Complex protocol:** Redlock is notoriously difficult to implement correctly. Clock skew, network delays, and node failures create edge cases.
- **Performance overhead:** Acquiring a lock requires 5 Redis calls (one per node) + retry logic. This adds 10-50ms latency to every operation.
- **Availability risk:** If we can't acquire a lock (Redis partition, clock skew, too much contention), operations block. This violates our availability goal.
- **Overkill for sentiment bar:** Redlock is designed for systems where inconsistency causes data loss or financial impact. For a sentiment bar, showing slightly stale data is acceptable.

**When this would make sense:**
- If we were processing payments (strong consistency required)
- If inconsistency caused user-visible bugs (it doesn't - stale data is OK)
- If cleanup had side effects that must happen exactly once (it doesn't - redundant cleanup is harmless)

### 2. Linearizable consistency with etcd/Raft

**Description:** Use a consensus protocol (etcd, Consul, Raft) for coordination. Elect a leader instance that handles all cleanup. Follower instances defer to the leader.

**Rejected because:**
- **Adds external dependency:** Requires deploying and managing etcd cluster or Consul. Increases system complexity and operational burden.
- **Single leader becomes bottleneck:** If the leader instance handles all cleanup, it becomes a single point of bottleneck. With distributed cleanup (every instance runs timer), load is distributed.
- **Leader election delay:** When a leader crashes, it takes time to elect a new leader (seconds). During this time, no cleanup happens. With our approach, every instance cleans up independently (no election needed).
- **Doesn't eliminate race conditions:** Even with leader election, we still have races (what if follower activates session while leader is cleaning up?). Leader election doesn't solve the fundamental coordination problem.

**When this would make sense:**
- If we needed total ordering of operations (we don't)
- If we needed to coordinate complex multi-step workflows (we don't - cleanup is a single DELETE operation)

### 3. Sacrifice availability - block on failures

**Description:** When we encounter an inconsistency or can't determine the correct state, block the request and return an error. Prioritize consistency over availability.

**Example:** If ref count is negative or cleanup can't acquire a lock, return 503 Service Unavailable.

**Rejected because:**
- **Poor user experience:** If the overlay goes dark (503 error) because of a transient ref count inconsistency, viewers notice. The streamer looks unprofessional.
- **Sentiment bar should degrade gracefully:** It's better to show slightly stale sentiment (eventual consistency) than to show nothing at all (hard failure).
- **Not appropriate for the use case:** This would make sense for a banking system (block on inconsistency to prevent money loss). For a sentiment bar, availability is more important than perfect consistency.

## Consequences

### ✅ Positive

- **Simpler code:** No lock management, no consensus protocols, no complex retry logic. Just best-effort operations (INCR, DECR, DELETE) + cleanup timers.
- **Faster operations:** No lock acquisition latency. Ref count operations are single Redis calls (<1ms). No quorum waits.
- **Better availability:** System continues to function despite race conditions, network partitions, or instance crashes. No blocking on failures.
- **Self-healing:** Cleanup timers automatically fix inconsistencies (orphaned keys, negative ref counts). No manual intervention or debugging required.
- **Easier to reason about:** Clear principle: "accept races, clean up with timers." Developers don't need to think about lock acquisition order or deadlock avoidance.

### ❌ Negative

- **Race conditions possible:** Ref count races, cleanup vs activation races, config staleness. These are inherent to eventual consistency.
- **Eventual consistency window:** Sessions may linger in Redis for up to 30s after all clients disconnect. Config changes may not take effect until next activation.
- **Debugging harder:** No single source of truth at any instant. Different instances may show different values for the same session (until they converge).
- **Requires education:** Developers must understand "eventual consistency" philosophy. Coming from a strong consistency background (SQL transactions), this is a mindset shift.

### 🔄 Trade-offs

- **Chose availability + simplicity over perfect consistency:** We could implement Redlock or etcd, but the complexity and performance overhead aren't justified for a sentiment bar. We accept eventual consistency for simpler code and better availability.
- **Accept 30s cleanup window for self-healing:** The grace period means orphaned sessions linger, but it also means the system automatically recovers from races. We chose resilience over immediacy.
- **Prioritize user experience (degrade gracefully) over strict correctness:** Showing slightly stale sentiment (eventual consistency) is better than showing nothing (hard failure). We optimize for the 99% case (everything works) over the 1% case (rare inconsistencies).

## Design Philosophy: Self-Healing Over Prevention

**Key insight:** In distributed systems, you can't prevent all failures and races. But you can design systems to automatically recover.

**Prevention approach (rejected):**
- Try to prevent all race conditions with locks
- Block on failures to maintain consistency
- Result: Complex code, brittle system, poor availability

**Self-healing approach (accepted):**
- Accept that races will happen
- Design cleanup mechanisms (timers, idempotency)
- Result: Simpler code, resilient system, better availability

**Examples of self-healing in ChatPulse:**

1. **Orphaned ref count keys:** Cleanup timer deletes them (30s window)
2. **Session deleted during activation:** Re-activation triggered automatically (milliseconds)
3. **Negative ref counts:** Treated as 0 by cleanup timer (harmless)
4. **Stale config in Redis:** Refreshed on next activation (from PostgreSQL)
5. **Twitch EventSub 409 Conflict:** Already subscribed = treat as success (idempotent)

## Related Decisions

- **ADR-006: Ref counting coordination** - Main example of eventual consistency. Ref count races are accepted and cleaned up by timers.
- **ADR-007: Database vs Redis separation** - PostgreSQL is source of truth (strong consistency), Redis is cache (eventual consistency).
- **ADR-001: Redis-only architecture** - Enables eventual consistency (shared state in Redis, no in-memory caches to sync).
- **ADR-005: Stateless instances** - Instances are independent, no leader election needed, eventual consistency via Redis.

## Observability

Monitor the following to detect consistency issues:

- **Orphan cleanup count:** How many sessions did the 30s timer clean up? Should be <10 typically. If >100, investigate (might indicate ref counting bug).
- **Negative ref counts:** Count of sessions with `ref_count < 0`. Should be 0 always. If >0, investigate race conditions.
- **Config staleness:** Time between config save (PostgreSQL) and Redis update. Should be <1s for active sessions. If >60s, investigate.
- **Re-activation rate:** Percentage of activations that are "cold start" (fetch from PostgreSQL) vs "warm" (found in Redis). High cold start rate might indicate excessive cleanup.

## When to Reconsider This Decision

This decision should be reconsidered if:

1. **Compliance requirements change:** If we start storing financial data or medical records (require strong consistency)
2. **Use case changes:** If the sentiment bar becomes write-heavy user interaction (require immediate consistency)
3. **Inconsistencies cause user-visible bugs:** If eventual consistency leads to confusing UX (it shouldn't, but monitor feedback)
4. **Scale increases dramatically:** At very large scale (1M+ sessions), 30s cleanup window might accumulate too much memory (consider shorter window or more aggressive cleanup)

However, for the current use case (sentiment bar for streamers), eventual consistency is the right choice.
