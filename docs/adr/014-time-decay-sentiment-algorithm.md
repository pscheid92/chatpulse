# ADR-014: Time-Decay Sentiment Algorithm

**Status:** Accepted
**Date:** 2026-02-12
**Authors:** Infrastructure Team
**Tags:** algorithm, domain-logic, redis

## Context

The sentiment bar represents chat sentiment (positive to negative on -100 to +100 scale). Without decay, the bar would stay at extremes indefinitely, making it boring for viewers and unresponsive to recent sentiment shifts.

### Requirements

1. **Smooth decay** - Continuous approach to neutral, not stepwise jumps
2. **Configurable** - Streamers choose decay speed (fast vs slow)
3. **Atomic** - Decay + vote application must be atomic (no race conditions)
4. **Time-accurate** - Same result regardless of query frequency

## Decision

Use **exponential decay** computed in Redis Lua functions. Formula:

```
decayed_value = current_value * e^(-decay_rate * time_elapsed)
```

Where:
- `current_value` = last known sentiment value
- `decay_rate` = streamer-configured speed (0.1 to 10.0 per second)
- `time_elapsed` = seconds since last update
- `e` = Euler's number (2.71828...)

### Implementation

**Redis Function (Lua):**
```lua
local function get_decayed_value(keys, args)
    local session_key = keys[1]
    local now_ms = tonumber(args[1])
    local decay_rate = tonumber(args[2])

    -- Read current state
    local value = tonumber(redis.call('HGET', session_key, 'value')) or 0
    local last_update = tonumber(redis.call('HGET', session_key, 'last_update')) or now_ms

    -- Compute time elapsed (convert ms to seconds)
    local dt_seconds = (now_ms - last_update) / 1000.0

    -- Apply exponential decay
    local decay_factor = math.exp(-decay_rate * dt_seconds)
    return value * decay_factor
end
```

**Vote application combines decay + delta:**
```lua
local function apply_vote(keys, args)
    local delta = tonumber(args[1])
    local decay_rate = tonumber(args[2])
    local now_ms = tonumber(args[3])

    -- Get decayed value
    local decayed = get_decayed_value(keys, {now_ms, decay_rate})

    -- Apply vote delta
    local new_value = math.max(-100, math.min(100, decayed + delta))

    -- Store new value and timestamp
    redis.call('HSET', keys[1], 'value', tostring(new_value), 'last_update', tostring(now_ms))
    return new_value
end
```

### Configuration Per Streamer

Stored in `configs` table, JSON-encoded in Redis session hash:

```go
type Config struct {
    DecaySpeed    float64 `json:"decay_speed"`    // Default: 1.0/second
    VoteDelta     float64 `json:"vote_delta"`     // Default: 10.0
    ForTrigger    string  `json:"for_trigger"`    // e.g., "PogChamp"
    AgainstTrigger string `json:"against_trigger"` // e.g., "NotLikeThis"
}
```

**Decay speed examples:**
- `0.1` = Very slow (takes ~20s to reach neutral from ±100)
- `1.0` = Standard (takes ~2s to reach neutral from ±100)
- `2.0` = Fast (takes ~1s to reach neutral from ±100)

## Alternatives Considered

### 1. Linear Decay

Subtract constant per second: `value -= decay_rate * dt`

**Example:** value=100, decay_rate=10/s, dt=5s → value=50

**Rejected because:**
- Reaches exactly zero (should approach neutral asymptotically)
- Discontinuous at zero crossing (jumps from +1 to 0 to -1)
- Doesn't feel natural (viewers expect smooth slowdown)

### 2. Step Decay

Decay in discrete steps every N seconds.

**Example:** Every 10 seconds, multiply by 0.9

**Rejected because:**
- Visible jumps (not smooth)
- Harder to configure (step size + interval = 2 params)
- Feels jarring (sudden changes)

### 3. No Decay

Sentiment stays until manual reset.

**Rejected because:**
- Bar stays at extremes (boring for viewers)
- Manual reset required (bad UX)
- Doesn't reflect recent sentiment (only historical)

### 4. Client-Side Calculation

Compute decay in browser JavaScript.

**Rejected because:**
- Clock sync issues (client time ≠ server time)
- Inconsistent across clients (different clock skew)
- Can't use for vote application (server must be source of truth)
- WebSocket latency causes visible jumps

## Consequences

### Positive

✅ **Smooth decay** - Continuous, asymptotic approach to neutral
✅ **Mathematically elegant** - Well-understood exponential function
✅ **Configurable** - Streamers choose decay speed
✅ **Atomic** - Lua function ensures no race conditions
✅ **Time-accurate** - Same result regardless of query frequency

### Negative

❌ **Clock sync required** - All instances must use same time source (NTP)
❌ **Lua testing complexity** - Need Redis to test algorithm
❌ **Exponential math** - Harder to understand than linear for non-technical users
❌ **Never reaches zero** - Asymptotic approach (may confuse some users)

### Trade-offs

🔄 **Smooth decay over simple linear** - Better UX > simpler math
🔄 **Server authority over client flexibility** - Consistency > client-side control
🔄 **Atomic Lua over app-level locking** - Performance > distributed locks

## Mathematical Properties

**Exponential decay characteristics:**

1. **Half-life:** Time to reach 50% of current value
   - Formula: `t_half = ln(2) / decay_rate`
   - Example: decay_rate=1.0 → t_half ≈ 0.69 seconds

2. **Time to neutral (99% decay from ±100):**
   - Formula: `t_99% = -ln(0.01) / decay_rate ≈ 4.6 / decay_rate`
   - Example: decay_rate=1.0 → t_99% ≈ 4.6 seconds

3. **Asymptotic approach:**
   - Never reaches exactly zero
   - After 5 half-lives: <3% of original value (effectively neutral)

## Clock Synchronization

**Current approach:** App passes `time.Now().UnixMilli()`

**Assumptions:**
- All instances use NTP (synchronized clocks)
- Clock drift negligible (<100ms)
- Redis and app servers on same NTP source

**Alternative considered:** Redis `TIME` command
- Pro: Single source of truth (Redis clock)
- Con: Extra roundtrip (1-2ms latency)
- **Verdict:** Not worth latency cost; NTP sufficient

## Edge Cases

**1. Inactive session (hours):**
- Problem: `math.exp(-1.0 * 7200)` = nearly zero (but very small number)
- Solution: Reset to 0 if `dt > 1 hour` (see input validation ADR)

**2. Clock skew (future timestamp):**
- Problem: `dt < 0` → decay factor > 1 (value grows!)
- Solution: `dt = max(0, now - last_update)` (clamp to zero)

**3. Extreme decay rate:**
- Problem: `math.exp(-1000 * dt)` = 0 (underflow)
- Solution: Clamp decay_rate to [0.1, 10.0] (see input validation ADR)

## Testing Strategy

**Unit tests (Lua):**
```lua
-- Test: Decay reduces value over time
value = 100, decay_rate = 1.0, dt = 1s
expected = 100 * e^(-1.0 * 1) ≈ 36.8

-- Test: No decay at dt=0
value = 50, decay_rate = 1.0, dt = 0s
expected = 50 (no change)

-- Test: Asymptotic approach
value = 100, decay_rate = 1.0, dt = 10s
expected ≈ 0 (effectively neutral)
```

**Integration tests (Go):**
```go
// Verify decay over time
store.ApplyVote(uuid, +100)  // Set to +100
time.Sleep(1 * time.Second)
value := store.GetSentiment(uuid)
assert.InDelta(t, 36.8, value, 5.0)  // Within ±5
```

## Related Decisions

- **ADR-001: Redis Functions** - Decay computed in Lua for atomicity
- **ADR-011: Actor Pattern** - Broadcaster polls decayed value every 50ms
- **Lua robustness epic** - Input validation handles edge cases

## Implementation

**Files:**
- `internal/redis/chatpulse.lua` - Lua functions (apply_vote, get_decayed_value)
- `internal/redis/sentiment_store.go` - Go wrapper calling Lua
- `internal/sentiment/engine.go` - Domain service orchestrating decay

**Commit:** Initial implementation (2025-12), enhanced validation (2026-02)

## UI/UX Implications

**Visible to streamers:**
- Dashboard config: "Decay Speed" slider (0.1 to 2.0)
- Visual: "Slow" ← slider → "Fast"
- Default: 1.0 (balanced)

**Visible to viewers:**
- Bar smoothly drifts toward neutral when no votes
- Recent votes matter more than old votes
- Feels responsive and dynamic

**Not visible:**
- Mathematical formula (implementation detail)
- Millisecond timestamps (abstracted away)

## References

- [Exponential Decay - Wikipedia](https://en.wikipedia.org/wiki/Exponential_decay)
- [Redis Functions](https://redis.io/docs/manual/programmability/functions-intro/)
- Half-life concept from physics (radioactive decay)
