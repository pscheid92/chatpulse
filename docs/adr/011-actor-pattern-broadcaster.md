# ADR-011: Actor Pattern for Broadcaster Concurrency

**Status:** Accepted
**Date:** 2026-02-12
**Authors:** Infrastructure Team
**Tags:** concurrency, architecture, implementation

## Context

The Broadcaster manages WebSocket connections and a 50ms tick loop that broadcasts sentiment values to all connected clients. It must handle concurrent operations safely:

- Multiple goroutines registering/unregistering clients
- Tick loop reading session state and broadcasting to clients
- Query operations (e.g., GetClientCount for testing)

The core challenge is safe access to the `activeClients` map: `map[uuid.UUID]sessionClients` where each session has multiple WebSocket connections.

### Requirements

1. **Thread safety** - Concurrent access from multiple goroutines
2. **No race conditions** - Map mutations must be atomic
3. **Testability** - Synchronous queries for assertions
4. **Performance** - 50ms tick budget, support 100+ sessions

## Decision

Use the **actor pattern** - a single goroutine owns all mutable state. All mutations happen through a typed command channel.

### Architecture

```go
type Broadcaster struct {
    cmdCh         chan broadcasterCmd   // Buffered channel (cap 256)
    activeClients map[uuid.UUID]sessionClients
    // No mutexes - single goroutine owns state
}

func (b *Broadcaster) run() {
    ticker := time.NewTicker(50 * time.Millisecond)
    for {
        select {
        case cmd := <-b.cmdCh:
            switch c := cmd.(type) {
            case *registerCmd:
                b.handleRegister(c)
            case *unregisterCmd:
                b.handleUnregister(c)
            case *getClientCountCmd:
                c.reply <- b.getClientCount()  // Synchronous reply
            }
        case <-ticker.C:
            b.handleTick()
        }
    }
}
```

### Command Pattern

Commands implement a marker interface:

```go
type broadcasterCmd interface {
    isBroadcasterCmd()
}

type registerCmd struct {
    baseBroadcasterCmd
    sessionUUID uuid.UUID
    conn        *websocket.Conn
}

type getClientCountCmd struct {
    baseBroadcasterCmd
    reply chan int  // Synchronous reply for testing
}
```

### Testability

Query commands use reply channels as **barrier calls** - they won't return until all prior commands are processed:

```go
// Test: verify client registration
count := broadcaster.GetClientCount()  // Blocks until register cmd processed
assert.Equal(t, 1, count)
```

## Alternatives Considered

### 1. Mutex-Protected Shared State

Traditional Go concurrency - protect map with sync.RWMutex.

```go
type Broadcaster struct {
    mu            sync.RWMutex
    activeClients map[uuid.UUID]sessionClients
}

func (b *Broadcaster) Register(uuid, conn) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.activeClients[uuid] = append(b.activeClients[uuid], conn)
}
```

**Rejected because:**
- Harder to reason about (lock ordering, potential deadlocks)
- More error-prone (forgot to lock, held lock too long)
- Testing harder (races between test and tick loop)

### 2. Channel Per Session

One actor goroutine per session.

**Rejected because:**
- N goroutines for N sessions (resource overhead)
- Tick loop coordination harder (who owns the timer?)
- More complex (need coordination between actors)

### 3. Separate Actors

One actor for client management, one for tick loop.

**Rejected because:**
- Two actors still need to share `activeClients` map
- Doesn't eliminate mutex burden (just moves it)
- Added complexity (two actors communicating)

## Consequences

### Positive

✅ **Simple reasoning** - Serial execution, no race conditions
✅ **No mutex bugs** - No deadlocks, forgot to unlock, lock ordering
✅ **Easy testing** - Synchronous barrier calls via reply channels
✅ **Clear ownership** - One goroutine owns all mutable state

### Negative

❌ **Single goroutine bottleneck** - All commands serial
❌ **Command processing latency** - Register waits for tick to finish
❌ **Channel capacity** - Must tune buffer size (256 commands)

### Trade-offs

🔄 **Simplicity over throughput** - Chose cleaner code over maximum concurrency
🔄 **Correctness over optimization** - No races > faster execution
🔄 **Testability over flexibility** - Synchronous queries for assertions

## Scaling Limits

**Current capacity:**
- Tested: 100 sessions, no issues
- Expected max: 500-1000 sessions per instance (50ms tick budget)

**Beyond 1000 sessions:**
- Tick loop may exceed 50ms budget
- Command channel may fill (backpressure)
- Consider: sharded broadcasters or horizontal scaling

## Related Decisions

- **ADR-002: Pull-based Broadcaster** - Consequence: tick loop lives in actor
- **Lua Function Version 2** - Returns status, version for monitoring

## Implementation

**Files:**
- `internal/broadcast/broadcaster.go` - Actor implementation
- `internal/broadcast/writer.go` - Per-connection write goroutines
- `internal/broadcast/broadcaster_test.go` - Tests using barrier calls

**Commit:** e3a7f21 (2026-01)

## References

- [Effective Go: Concurrency](https://go.dev/doc/effective_go#concurrency)
- "Share Memory By Communicating" - Go proverb
- Actor model pattern in Erlang/Elixir influence
