# ADR-001: Redis-only architecture for session state

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse is a multi-tenant SaaS that tracks real-time chat sentiment for Twitch streamers. The system must support horizontal scaling across multiple instances to handle growth. Key requirements:

- **Multiple instances** must serve requests concurrently (any instance can serve any session)
- **Session state** (sentiment value, config, last update timestamp) must be shared across instances
- **50ms broadcast latency** - the Broadcaster polls current values every 50ms and sends to WebSocket clients
- **Instance crashes** must not lose active session state
- **Zero warm-up time** - new instances should serve traffic immediately after startup

The challenge: how do we share session state across instances efficiently?

## Decision

**All session state lives in Redis. PostgreSQL stores only durable configuration and user data.**

Specifically:

- **Redis stores:** Active sessions, current sentiment values, last update timestamps, ref counts (which instances serve which sessions), broadcaster-to-session mappings
- **PostgreSQL stores:** User profiles, OAuth tokens, persisted configuration (triggers, labels, decay speed)
- **Instances are completely stateless** - no in-memory session state. Any instance can serve any session by reading from Redis.
- **Redis Functions** (`apply_vote`, `get_decayed_value`) handle atomic vote operations and time-decay calculations
- **Ref counting** tracks how many instances serve each session (enables cleanup when ref count reaches 0)

**Key schema in Redis:**
```
session:{overlayUUID} → hash (value, broadcaster_user_id, config_json, last_update, last_disconnect)
ref_count:{overlayUUID} → integer
broadcaster:{twitchUserID} → overlayUUID
debounce:{overlayUUID}:{twitchUserID} → key with 1s TTL
```

## Alternatives Considered

### 1. Sticky sessions (load balancer pins user to instance)

**Description:** Configure the load balancer to route each user consistently to the same instance based on session cookie or overlay UUID.

**Rejected because:**
- **Doesn't solve cleanup on crash:** When an instance crashes, all its sessions are lost. The load balancer has no way to know which sessions were active on the crashed instance.
- **Uneven load distribution:** If one streamer has 10K viewers and another has 10, the instance serving the popular streamer becomes overloaded while others sit idle.
- **Complex routing rules:** Requires load balancer configuration that couples infrastructure to application logic.
- **Manual rebalancing required:** Adding/removing instances requires careful session migration to avoid disrupting active overlays.

### 2. In-memory state with Redis pub/sub synchronization

**Description:** Each instance keeps session state in local memory and publishes updates via Redis pub/sub. Other instances subscribe and update their local state.

**Rejected because:**
- **Complex consistency model:** Race conditions between pub/sub messages and direct reads. What happens if pub/sub lags behind direct Redis reads?
- **Memory usage scales with total sessions, not active:** Each instance must hold state for *all* sessions in the system (not just the ones it serves), otherwise it can't serve requests that land on it after a session moves.
- **Warm-up time after restarts:** New instances must wait to receive pub/sub updates for all active sessions before serving traffic, or risk serving stale data.
- **Harder to reason about:** Two sources of truth (local memory + Redis) with synchronization protocol. Debugging "why is this instance showing wrong value?" becomes much harder.

### 3. Distributed cache (Hazelcast, Coherence, etc.)

**Description:** Use a JVM-style distributed data structure library that handles state replication across instances automatically.

**Rejected because:**
- **Go ecosystem not mature:** Hazelcast has no official Go client. Coherence is Java-only. Would need to evaluate less-proven solutions.
- **More moving parts than Redis:** Adds another technology to learn, deploy, and operate. Redis is already in the stack and well-understood.
- **Overkill for current scale:** These systems are designed for complex distributed transactions and ACID guarantees. We just need simple key-value lookups with atomic operations.
- **Operations complexity:** Requires tuning partition counts, quorum settings, split-brain handling, etc.

### 4. Database-only (PostgreSQL for everything)

**Description:** Store session state directly in PostgreSQL. Read from DB on every broadcast tick.

**Rejected because:**
- **Too slow for 50ms tick loop:** PostgreSQL query latency (even with connection pooling) is 5-20ms. Reading N sessions per tick would bottleneck quickly.
- **Write load on database:** Every vote writes to PostgreSQL, increasing load on the most critical persistence layer.
- **Polling creates contention:** Multiple instances polling same rows every 50ms creates lock contention and unnecessary load.

## Consequences

### ✅ Positive

- **True stateless instances:** Any instance can serve any request. No need for session affinity, sticky sessions, or complex routing.
- **Horizontal scaling is trivial:** Adding a new instance is literally `docker run` - no warm-up, no rebalancing, no configuration changes.
- **Instance crashes don't lose state:** If an instance dies, active sessions continue on other instances. Ref counting ensures cleanup happens correctly.
- **Simple deployment:** No warm-up period, no careful orchestration. Just start the instance and it's ready.
- **Redis expertise is common:** Most backend engineers know Redis. Hiring and onboarding are easier.
- **Clear data separation:** Redis = ephemeral session state. PostgreSQL = durable config/users. Easy to reason about what goes where.

### ❌ Negative

- **2-5ms network latency per Redis operation:** Every read/write requires a network roundtrip. Can't optimize with local caching without giving up consistency.
- **Requires Redis HA in production:** Redis becomes a single point of failure. Must set up Redis Sentinel or Redis Cluster for high availability.
- **Redis memory limits:** All session state must fit in Redis memory. Need to monitor memory usage and evict inactive sessions.
- **More complex than in-memory for single-instance deployments:** If running locally or on a single small instance, Redis adds overhead compared to a simple in-memory map.

### 🔄 Trade-offs

- **Chose availability + scalability over latency:** We accept 2-5ms Redis latency (vs <1ms in-memory) to gain horizontal scaling and crash recovery.
- **Chose simplicity over optimization:** We could cache values locally and invalidate on updates (in-memory + Redis), but that adds complexity. We chose to always read from Redis for simpler code.
- **Chose eventual consistency for ref counting:** Ref counts are incremented/decremented on session connect/disconnect, but cleanup happens 30s after disconnect. This 30s window allows for eventual consistency without complex distributed transactions.
- **Accepted 50ms staleness (pull-based):** We read from Redis every 50ms instead of real-time pub/sub. This trade-off is documented in ADR-002.

## Related Decisions

- **ADR-002: Pull-based broadcaster** - Consequence of Redis-only: broadcaster makes N Redis calls per tick (one per active session)
- **ADR-005: No sticky sessions** (future) - Consequence: need shared state (this decision)
- **ADR-006: Ref counting strategy** (future) - Consequence: need to track which instances serve which sessions
- **ADR-007: Database vs Redis data separation** (future) - Design principle: Redis = ephemeral, PostgreSQL = durable
