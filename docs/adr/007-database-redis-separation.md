# ADR-007: Database vs Redis data separation

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse uses two storage systems:
- **PostgreSQL:** Relational database with ACID guarantees, durable storage, SQL queries
- **Redis:** In-memory key-value store, fast reads/writes (~1-2ms), ephemeral by default

We need to decide what data lives in each system. Key questions:
- Where do we store user profiles and OAuth tokens?
- Where do we store sentiment configuration (triggers, labels, decay speed)?
- Where do we store active session state (current sentiment value, last update)?
- What happens on cold start (instance restarts, Redis empty)?
- What happens on config change (streamer updates triggers)?

**Performance constraints:**
- The Broadcaster tick loop runs every 50ms and reads all active sessions
- PostgreSQL query latency: 5-20ms (typical for SELECT with WHERE clause)
- Redis query latency: 1-2ms (typical for single key GET/HSET)

**Durability constraints:**
- User data must survive instance/Redis restarts (durable storage required)
- Session state can be reconstructed from PostgreSQL (ephemeral is OK)
- Lost Redis data = inconvenience (cold start), not data loss

## Decision

**PostgreSQL is the source of truth for durable data. Redis is an ephemeral cache for hot session state.**

Specifically:

### PostgreSQL Stores (durable):
- **Users:** Twitch OAuth data (user ID, username, display name), access/refresh tokens (encrypted at rest), token expiry, overlay UUID
- **Config:** Sentiment settings per user (for/against triggers, labels, decay speed, vote delta)
- **EventSub subscriptions:** Active Twitch EventSub subscriptions (for idempotency + crash recovery)

### Redis Stores (ephemeral):
- **Sessions:** Current sentiment value, config snapshot (JSON), broadcaster mapping, last update timestamp, last disconnect timestamp, ref count
- **Sentiment:** Time-decaying value (computed by Redis Functions on read)
- **Debounce:** Per-user vote rate limiting (1s TTL, auto-expires)

### Cold Start Flow (instance starts, Redis empty):

1. **WebSocket connects:** `GET /ws/overlay/:uuid`
2. **Lookup user:** `SELECT * FROM users WHERE overlay_uuid = $1` (PostgreSQL)
3. **Fetch config:** `SELECT * FROM configs WHERE user_id = $1` (PostgreSQL)
4. **Activate session in Redis:**
   ```
   HSET session:{uuid} value 0
   HSET session:{uuid} config_json {JSON serialized config}
   HSET session:{uuid} broadcaster_user_id {twitch_id}
   HSET session:{uuid} last_update {now}
   SET broadcaster:{twitch_id} {uuid}
   ```
5. **Subscribe to Twitch EventSub:** `CreateEventSubSubscription(uuid, broadcaster_id)`
6. **Now session is hot:** All subsequent reads come from Redis (~2ms latency)

### Config Change Flow (streamer updates triggers):

1. **Dashboard POST:** `/dashboard/config` with new triggers
2. **Update PostgreSQL:** `UPDATE configs SET for_trigger = $1, ... WHERE user_id = $2`
3. **Update Redis (if session active):**
   ```
   HGET session:{uuid} last_update  // Check if session exists
   HSET session:{uuid} config_json {new JSON}
   ```
4. **If session inactive:** Do nothing. Config will be fetched from PostgreSQL on next activation.

**Key principle:** PostgreSQL is always the source of truth. Redis is a cache. If Redis and PostgreSQL disagree, PostgreSQL wins.

## Alternatives Considered

### 1. All data in PostgreSQL (no Redis)

**Description:** Store session state (current sentiment value, last update) in PostgreSQL. Read from DB on every Broadcaster tick (50ms).

**Rejected because:**
- **Too slow for 50ms tick loop:** PostgreSQL SELECT latency is 5-20ms (depending on connection pool, network, query complexity). Reading N sessions per tick would bottleneck quickly.
- **Write load on database:** Every vote writes to PostgreSQL (`UPDATE sessions SET value = ... WHERE uuid = ...`). With high vote rates (10+ votes/second per popular stream), this creates significant write load on the most critical persistence layer.
- **Polling creates contention:** Multiple instances polling the same rows every 50ms → row-level locking, cache invalidation churn, unnecessary load.
- **Can't use Redis Functions:** Time-decay calculation (`get_decayed_value`) is implemented as a Redis Function for atomic read + compute. Moving to PostgreSQL would require PL/pgSQL or fetching + computing in Go (both slower).

**When this would make sense:**
- If tick frequency was much lower (e.g., 5-second updates instead of 50ms)
- If vote rate was very low (e.g., <1 vote/minute)

### 2. All data in Redis (no PostgreSQL)

**Description:** Store users, config, and session state entirely in Redis. Use Redis persistence (RDB snapshots or AOF log) for durability.

**Rejected because:**
- **Lose data on Redis failure:** RDB snapshots are periodic (e.g., every 5 minutes). If Redis crashes between snapshots, we lose up to 5 minutes of config changes, new user signups, etc. AOF is more durable but slower (fsync on every write).
- **Harder to query:** Redis is key-value only. Queries like "find all users who signed up this month" or "list all EventSub subscriptions" require scanning all keys (slow) or maintaining secondary indexes (complex).
- **Backups more complex:** PostgreSQL backups are well-understood (pg_dump, WAL archiving, point-in-time recovery). Redis backups (RDB/AOF) are less mature for operational use (e.g., restoring to exact point in time is harder).
- **Schema evolution harder:** PostgreSQL migrations (via tern) handle schema changes gracefully (ADD COLUMN, CREATE INDEX). Redis doesn't have a schema - we'd need application-level migration logic (scan all keys, rewrite values).

**When this would make sense:**
- If durability wasn't important (e.g., demo/prototype)
- If all data was truly ephemeral (e.g., short-lived sessions with no user accounts)

### 3. Dual-write to both (PostgreSQL + Redis on every config change)

**Description:** When a streamer updates config, write to both PostgreSQL and Redis simultaneously. Always read from Redis (never query PostgreSQL).

**Rejected because:**
- **Consistency issues:** What if PostgreSQL write succeeds but Redis write fails (or vice versa)? Which is the source of truth? Do we retry? Do we roll back? Distributed transactions are complex.
- **More complex code:** Every config update now requires two writes (PostgreSQL + Redis) + error handling for partial failures.
- **PostgreSQL is already source of truth:** On cold start, we fetch config from PostgreSQL. So PostgreSQL is implicitly the source of truth. Dual-writing just adds complexity without changing this fact.
- **Redis update is already there:** We already update Redis when config changes (see "Config Change Flow" above). This approach would make it mandatory instead of opportunistic.

**When this would make sense:**
- If reading from PostgreSQL on cold start was unacceptable (it's not - 10-20ms is fine)

## Consequences

### ✅ Positive

- **Clear separation:** PostgreSQL = durable, Redis = fast. Easy to reason about "what goes where."
- **PostgreSQL is source of truth:** Config changes persist across restarts. Redis can be flushed with zero data loss (just cold starts).
- **Redis optimized for hot path:** 50ms tick loop reads from Redis (~2ms latency). No database queries in hot path.
- **Lose Redis data? Just reactivate:** If Redis crashes or is flushed, sessions can be reconstructed from PostgreSQL. No permanent data loss.
- **Schema evolution in PostgreSQL:** Migrations (tern) handle schema changes cleanly. Redis data is schemaless (JSON strings).

### ❌ Negative

- **Cold start latency:** When an instance serves a session for the first time, it must fetch user + config from PostgreSQL (~10-20ms). This is slower than reading from Redis (~2ms).
- **Two storage systems to monitor:** Need to monitor PostgreSQL (disk space, connection pool, query latency) and Redis (memory usage, eviction policy, hit rate).
- **Config changes not real-time in Redis:** If a streamer updates config while no one is watching their overlay, the change stays in PostgreSQL. Next activation fetches the new config. (This is acceptable - config changes are rare, and cold start is fast.)
- **Potential for Redis <-> PostgreSQL desync:** If we have a bug in the config update code (update PostgreSQL but forget to update Redis), active sessions might show stale config until next activation. This is mitigated by the fact that PostgreSQL is always fetched on cold start.

### 🔄 Trade-offs

- **Chose performance (Redis hot path) over simplicity (single DB):** We could use only PostgreSQL and simplify operations, but the 50ms tick loop wouldn't be feasible. We accept operational complexity (two storage systems) for performance.
- **Accept cold start penalty for optimized steady-state:** Initial WebSocket connection is 10-20ms slower (PostgreSQL fetch), but steady-state operations are 2-5ms (Redis). We optimize for the 99% case (steady-state) over the 1% case (cold start).
- **Prioritize durability (PostgreSQL) over real-time config updates:** Config changes in PostgreSQL are durable immediately. Redis update is opportunistic (only if session active). We chose durability guarantees over instant Redis consistency.

## Related Decisions

- **ADR-001: Redis-only architecture** - Context: "Redis-only" refers to session state (hot path), not all data. PostgreSQL still stores durable config/users.
- **ADR-005: Stateless instances** - Consequence: cold starts are common (every new instance serving a session). This decision accepts cold start latency for stateless benefits.
- **ADR-009: Token encryption at rest** (future) - Context: OAuth tokens stored in PostgreSQL are encrypted at rest (not plaintext).

## Implementation Notes

**Cold start optimization:**
The app uses singleflight (`golang.org/x/sync/singleflight`) to collapse concurrent cold starts. If 10 clients connect to the same session simultaneously (all cold), only one fetches from PostgreSQL. The other 9 wait for the result.

**Config snapshot in Redis:**
Redis stores a JSON snapshot of the config (`config_json` field in session hash). This avoids joining Redis + PostgreSQL on every tick. The trade-off is that config updates require updating Redis (if session active).

**Ref counting in Redis:**
Ref counts (`ref_count:{uuid}`) are stored in Redis, not PostgreSQL. These are coordination metadata (how many instances serve each session), not durable data. Losing ref counts just means orphaned sessions (cleaned by 30s timer).

**EventSub subscriptions in PostgreSQL:**
EventSub subscription IDs are stored in PostgreSQL (`eventsub_subscriptions` table) for idempotency (don't re-subscribe if already subscribed) and crash recovery (re-subscribe on startup if missing). This is durable data because Twitch rate limits subscription creation (avoid hitting limits by tracking what's already subscribed).

## Future Considerations

If cold start latency becomes a bottleneck (e.g., P95 latency spikes on instance startup), we could:

1. **Preload config cache:** On instance startup, fetch the top N most active sessions from Redis (`SCAN session:*`) and preload their configs from PostgreSQL. This would reduce cold starts for popular sessions.
2. **Config cache in Redis with TTL:** Store configs in Redis with 5-minute TTL (`SET config:{user_id} {json} EX 300`). Cold starts would read from cache (2ms) instead of PostgreSQL (10-20ms). Trade-off is slightly stale configs (up to 5 minutes old).
3. **Read replicas for PostgreSQL:** Route cold start queries to PostgreSQL read replicas (reduce load on primary). This improves scalability but doesn't reduce latency.

However, these optimizations add complexity. Current approach (PostgreSQL source of truth, Redis cache, singleflight for concurrent cold starts) is sufficient for foreseeable scale.

## Observability

Monitor the following to detect issues with data separation:

- **Cold start rate:** Percentage of `EnsureSessionActive` calls that hit PostgreSQL (vs Redis cache hit). Track this by user/session to identify hot sessions that should be preloaded.
- **Redis memory usage:** Total memory used by Redis. If approaching limit, investigate (might need to tune eviction policy or increase memory).
- **PostgreSQL query latency:** P50/P95/P99 latency for user/config queries. If P99 > 50ms, investigate (might need connection pool tuning or indexing).
- **Config desync events:** Count how many times we update PostgreSQL config but fail to update Redis (should be 0, indicates a bug).
