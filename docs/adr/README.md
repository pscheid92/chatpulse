# Architecture Decision Records (ADRs)

This directory contains Architecture Decision Records (ADRs) documenting the key architectural decisions made in the ChatPulse project.

## What are ADRs?

ADRs are documents that capture important architectural decisions along with their context, alternatives considered, and consequences. They help developers understand:

- **Why** a decision was made (not just what was decided)
- **What alternatives** were considered and why they were rejected
- **What trade-offs** were accepted
- **What consequences** (positive and negative) resulted from the decision

## ADR Template

Each ADR follows this structure:

```markdown
# ADR-XXX: [Title]

**Status:** Accepted | Deprecated | Superseded by ADR-YYY

**Date:** YYYY-MM-DD

## Context

What is the issue or situation that is motivating this decision?

## Decision

What architectural decision did we make?

## Alternatives Considered

What other options did we evaluate? Why were they rejected?

1. **Alternative Name** - Description
   - Rejected: Reason 1
   - Rejected: Reason 2

## Consequences

What are the positive, negative, and trade-off consequences of this decision?

✅ **Positive:**
- Good outcome 1
- Good outcome 2

❌ **Negative:**
- Drawback 1
- Drawback 2

🔄 **Trade-offs:**
- We chose X over Y because...

## Related Decisions

- ADR-XXX: Related decision
- ADR-YYY: Another related decision
```

## ADR Index

### Tier 1: Foundation ADRs

**Read these first** - they explain the core architectural decisions that all other design choices depend on:

| ADR | Title | Summary |
|-----|-------|---------|
| [ADR-001](001-redis-only-architecture.md) | Redis-only architecture for session state | All session state lives in Redis; PostgreSQL only for durable config. Enables true stateless horizontal scaling. |
| [ADR-002](002-pull-based-broadcaster.md) | Pull-based broadcaster vs Redis pub/sub | Broadcaster polls Redis every 50ms for current values instead of subscribing to push events. Simpler code, always fresh. |
| [ADR-003](003-eventsub-webhooks-conduits.md) | Twitch EventSub webhooks + conduits | Use Twitch webhooks (POST to our endpoint) instead of EventSub WebSocket. Reduced from 660 lines to ~150 lines. |

### Tier 2: Scaling ADRs

**Read after Tier 1** - these explain how multiple instances coordinate and scale:

| ADR | Title | Summary |
|-----|-------|---------|
| [ADR-004](004-single-bot-account.md) | Single bot account reads all channels | One dedicated bot account reads chat for all streamers. Streamers only grant `channel:bot` scope (minimal permissions). |
| [ADR-005](005-stateless-instances.md) | No sticky sessions - stateless instances | Any instance can serve any session. Load balancer uses round-robin. No session affinity. |
| [ADR-006](006-ref-counting-coordination.md) | Ref counting for multi-instance coordination | Redis INCR/DECR for ref counts. Eventual consistency with 30s cleanup timer. Accepts race conditions. |
| [ADR-007](007-database-redis-separation.md) | Database vs Redis data separation | PostgreSQL = durable source of truth (users, config). Redis = ephemeral cache (hot session state). |

### Tier 3: Security & Resilience ADRs

**Read after Tier 1 & 2** - these explain security boundaries and failure handling philosophy:

| ADR | Title | Summary |
|-----|-------|---------|
| [ADR-008](008-uuid-overlay-access-control.md) | UUID-based overlay access control | Separate `overlay_uuid` acts as bearer token. Public read access (zero-config OBS). Rotation invalidates old URLs. |
| [ADR-009](009-token-encryption-at-rest.md) | AES-256-GCM token encryption at rest | OAuth tokens encrypted before PostgreSQL storage. `crypto.Service` interface with AES-256-GCM (prod) or NoopService (dev). |
| [ADR-010](010-eventual-consistency-philosophy.md) | Graceful degradation - eventual consistency | Accept eventual consistency over strong consistency. Prioritize availability. Self-healing (cleanup timers) over prevention (locks). |

## How to Propose New ADRs

1. Copy the template above into a new file `XXX-title.md` (use next available number)
2. Fill in all sections, especially **Alternatives Considered** (explain rejections clearly)
3. Submit as PR for team review
4. Once accepted, add to index table above

## Updating Existing ADRs

ADRs should not be modified after acceptance (they are historical records). Instead:

- If a decision is **reversed**, mark the original ADR as "Superseded by ADR-XXX" and write a new ADR explaining the reversal
- If a decision is **deprecated**, mark it as "Deprecated" with a note explaining why
- Minor corrections (typos, clarifications) are acceptable as long as they don't change the decision
