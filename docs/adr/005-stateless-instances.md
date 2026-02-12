# ADR-005: No sticky sessions - stateless instances

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse is designed to run as multiple horizontally-scaled instances behind a load balancer. When a user's browser connects to the WebSocket endpoint (`/ws/overlay/:uuid`), the load balancer must route the connection to one of the instances. There are two fundamental approaches:

1. **Sticky sessions:** The load balancer ensures all connections for a given session/user go to the same instance (via IP hash, cookie, or session ID)
2. **Stateless routing:** Any instance can handle any request. The load balancer can use round-robin, least-connections, or any algorithm.

With the Redis-only architecture (ADR-001), all session state lives in Redis. This enables a choice: do we still want sticky sessions for performance reasons, or do we embrace fully stateless instances?

## Decision

**Any instance can serve any session. No sticky sessions, no session affinity.**

Specifically:

- **Load balancer routing:** Use round-robin, least-connections, or any algorithm that distributes load evenly. Do NOT configure IP hash, cookie-based routing, or any form of session affinity.
- **WebSocket connection handling:** When a WebSocket connects to `/ws/overlay/:uuid`, the instance that receives the connection pulls the session state from Redis (regardless of which instance served previous connections for that UUID).
- **Ref counting for coordination:** Because multiple instances may serve the same session simultaneously, we use ref counting (`IncrRefCount`/`DecrRefCount`) to coordinate cleanup (documented in ADR-006).
- **Cold start on first connect:** When an instance serves a session for the first time, it fetches the user + config from PostgreSQL and activates the session in Redis. Subsequent connections (on any instance) read from the Redis cache.

**Code behavior:**
```go
// handlers_overlay.go - WebSocket upgrade
func (s *Server) handleOverlayWebSocket(c echo.Context) error {
    uuid := c.Param("uuid")

    // Any instance can serve this - no check for "am I the right instance?"
    user, err := s.appSvc.GetUserByOverlayUUID(ctx, uuid)  // PostgreSQL (cold) or Redis (warm)

    session, err := s.appSvc.EnsureSessionActive(ctx, user.ID)  // Fetch config, activate in Redis
    s.appSvc.IncrRefCount(ctx, uuid)  // Tell Redis: "I'm serving this session"

    // ... WebSocket upgrade ...
}
```

## Alternatives Considered

### 1. Sticky sessions via load balancer (IP hash or cookie-based routing)

**Description:** Configure the load balancer (e.g., nginx, HAProxy, AWS ALB) to route requests for a given session/user to the same instance. Common strategies:
- **IP hash:** Hash the client IP, route to instance based on hash
- **Cookie-based:** Load balancer sets a cookie on first request, routes based on cookie value
- **Session ID in URL:** Route based on session UUID in the path

**Rejected because:**
- **Doesn't solve cleanup when instance crashes:** If the "sticky" instance dies, the load balancer routes new connections to a different instance. But the crashed instance can't decrement ref counts or clean up Redis state. We'd still need ref counting (ADR-006) to handle this case, so stickiness doesn't eliminate the coordination complexity.
- **Uneven load distribution:** If one streamer has 10,000 concurrent viewers (all with their browsers open to the overlay), all 10,000 WebSocket connections go to the same instance. Other instances sit idle. This defeats the purpose of horizontal scaling.
- **Complex failover:** When an instance crashes, the load balancer must re-route all "sticky" sessions to other instances. During the re-route, those sessions experience a connection drop and must reconnect. With stateless routing, failover is instant (the next request just goes to a healthy instance).
- **Contradicts Redis-only architecture goal:** ADR-001 chose Redis-only specifically to enable stateless instances. Adding sticky sessions re-introduces state affinity, which defeats the benefit.

**When this would make sense:**
- If we had expensive per-session state that couldn't fit in Redis (e.g., large ML models loaded per session)
- If Redis latency was unacceptable and we needed in-memory caching per instance (but then we'd have cache invalidation complexity)

### 2. Session affinity with consistent hashing (route by session UUID hash)

**Description:** Hash the overlay UUID and use the hash to determine which instance should serve the session. All connections for `overlay-123` always go to instance N (where N = hash(123) % num_instances).

**Rejected because:**
- **Same problems as sticky sessions:** Uneven load distribution (popular streamers pin to one instance), complex failover, doesn't eliminate cleanup coordination.
- **Instance removal disrupts sessions:** If instance 3 (of 5) crashes, all sessions that were hashed to instance 3 must be re-hashed. This means re-activating sessions, re-fetching config, and re-subscribing to EventSub. With stateless routing, removing an instance has zero impact on active sessions.
- **Harder to add/remove instances:** Consistent hashing algorithms (like Maglev) reduce disruption when adding/removing instances, but they don't eliminate it. With stateless routing, adding instance 6 is literally `docker run` - no re-balancing, no hash ring updates.

### 3. Dedicated instance per streamer (manual assignment in config)

**Description:** Assign each streamer to a specific instance (e.g., "streamer A → instance 1, streamer B → instance 2"). Operator manually updates the assignment table when adding instances.

**Rejected because:**
- **Manual ops burden:** Every time we add/remove an instance, the operator must update the assignment table and ensure streamers are evenly distributed. This doesn't scale.
- **Doesn't scale with demand:** If streamer A suddenly gets 10x viewers (e.g., viral clip, raid), their assigned instance becomes overloaded. We can't dynamically shift load without manual intervention.
- **Worse than sticky sessions:** All the problems of sticky sessions, plus manual configuration overhead.

**Why we considered this:**
- Initial thought: "Pre-assigning would avoid coordination complexity"
- Reality: Manual assignment is just sticky sessions with extra steps

## Consequences

### ✅ Positive

- **Simple load balancing:** Any algorithm works (round-robin, least-connections, random). No special configuration needed. Standard load balancers "just work."
- **Even load distribution:** 10,000 viewers watching the same streamer's overlay → 10,000 WebSocket connections distributed evenly across all instances. No hot instances.
- **Easy failover:** Instance crashes → next WebSocket connection goes to a different instance. Zero configuration. No session migration. No hash ring updates.
- **Instance crashes don't lose sessions:** Session state lives in Redis. When instance 3 crashes, instance 4 can immediately serve the session by reading from Redis. No data loss.
- **Trivial horizontal scaling:** Adding instance 6 is `docker run`. No config changes. No rebalancing. Load balancer automatically includes it in rotation.

### ❌ Negative

- **Requires ref counting for cleanup coordination:** Because multiple instances might serve the same session (e.g., user refreshes browser, gets routed to different instance), we need ref counting to coordinate cleanup. This adds complexity (documented in ADR-006).
- **Eventual consistency for ref count:** Ref counting has race conditions (two instances decrement simultaneously, orphaned sessions, etc.). We accept these and rely on a 30s cleanup timer for eventual consistency.
- **Cold start latency:** When an instance serves a session for the first time, it must fetch user + config from PostgreSQL. This adds ~10-20ms latency compared to an in-memory cache. Subsequent reads come from Redis (~2ms).

### 🔄 Trade-offs

- **Chose operational simplicity over optimal latency:** We accept ~10-20ms cold start latency (PostgreSQL read) to avoid the complexity of in-memory caching + cache invalidation across instances. 10-20ms is imperceptible for WebSocket connection setup.
- **Accept 30s cleanup delay for ref count races:** Orphaned sessions (from ref counting race conditions) linger in Redis for up to 30 seconds before the cleanup timer removes them. This is acceptable - Redis memory is cheap, and 30s is short enough that it doesn't accumulate.
- **Prioritize horizontal scaling over session affinity optimization:** We could optimize latency with sticky sessions, but we'd sacrifice even load distribution and easy failover. We chose to optimize for the 99% case (even load) over the 1% case (cold start latency).

## Related Decisions

- **ADR-001: Redis-only architecture** - Foundation: all session state in Redis enables stateless instances. This decision wouldn't be possible without ADR-001.
- **ADR-006: Ref counting strategy** - Consequence: stateless instances require ref counting to coordinate cleanup. Multiple instances can serve the same session, so we need a way to know when *all* instances are done with it.
- **ADR-007: Database vs Redis separation** - Context: cold start fetches config from PostgreSQL, then caches in Redis. Stateless instances mean cold starts are common (every new instance serving a session).

## Implementation Notes

**Load balancer configuration:**
- **nginx:** `upstream` block with default round-robin
- **HAProxy:** `balance roundrobin` or `balance leastconn`
- **AWS ALB:** Default routing (no target group stickiness)
- **Kubernetes:** Service with default round-robin (no session affinity)

**Cold start optimization:**
In the future, we could optimize cold starts by:

1. **Proactive session activation:** On instance startup, fetch the top N most active sessions from Redis and pre-activate them (fetch config from PostgreSQL). This would reduce cold start latency for popular sessions.
2. **Lazy config caching:** Instead of fetching config from PostgreSQL on every cold start, cache configs in Redis with a 5-minute TTL. This would reduce PostgreSQL load at the cost of slightly stale configs.

However, these optimizations add complexity. We'll wait for profiling data (e.g., "cold starts are 5% of all WebSocket connects and causing P95 latency spikes") before implementing.

## Observability

Monitor the following metrics to detect issues with stateless routing:

- **Cold start rate:** Percentage of `EnsureSessionActive` calls that hit PostgreSQL (vs Redis cache hit). High rate = many instances serving many unique sessions (good) or cache churn (investigate).
- **Ref count distribution:** Histogram of ref counts per session. Should be 0-2 for most sessions (one instance serving). If many sessions have ref_count > 5, investigate load balancer routing (might be misconfigured to round-robin per connection instead of per session).
- **Cleanup timer backlog:** How many orphaned sessions does the 30s cleanup timer find per run? Should be <10 typically. If >100, investigate ref counting bugs.
