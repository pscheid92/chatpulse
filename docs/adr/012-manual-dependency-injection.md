# ADR-012: Manual Dependency Injection

**Status:** Accepted
**Date:** 2026-02-12
**Authors:** Infrastructure Team
**Tags:** architecture, implementation, maintainability

## Context

The application has 10-15 dependencies that need wiring at startup:
- Database connection pool
- Redis client
- Repositories (User, Config, EventSub)
- Domain services (Engine, App, Broadcaster)
- HTTP server with handlers

### Requirements

1. **Explicit wiring** - Clear dependency graph
2. **IDE-friendly** - "Go to definition" must work
3. **Testability** - Easy to construct dependencies with mocks
4. **Zero magic** - No reflection, no code generation

## Decision

Use **manual dependency injection** in `cmd/server/main.go` with explicit constructor calls in dependency order.

### Example

```go
func main() {
    // 1. Infrastructure
    pool := database.Connect(ctx, cfg.DatabaseURL)
    rdb := redis.NewClient(ctx, cfg.RedisURL)
    clock := clockwork.NewRealClock()

    // 2. Crypto service (conditional)
    var cryptoSvc crypto.Service = crypto.NoopService{}
    if cfg.TokenEncryptionKey != "" {
        cryptoSvc, _ = crypto.NewAesGcmCryptoService(cfg.TokenEncryptionKey)
    }

    // 3. Repositories
    userRepo := database.NewUserRepo(pool, cryptoSvc)
    configRepo := database.NewConfigRepo(pool)
    eventSubRepo := database.NewEventSubRepo(pool)
    sessionRepo := redis.NewSessionRepo(rdb, clock)
    sentimentStore := redis.NewSentimentStore(rdb)
    debouncer := redis.NewDebouncer(rdb)
    voteRateLimiter := redis.NewVoteRateLimiter(rdb, clock, cfg.VoteRateLimitCapacity, cfg.VoteRateLimitRate)

    // 4. Domain services
    configCache := sentiment.NewConfigCache(10*time.Second, clock)
    engine := sentiment.NewEngine(sessionRepo, sentimentStore, debouncer, voteRateLimiter, clock, configCache)

    // 5. Twitch integration (optional)
    var twitchSvc domain.TwitchService
    if cfg.WebhookCallbackURL != "" {
        eventsubMgr, _ := twitch.NewEventSubManager(...)
        twitchSvc = eventsubMgr
    }

    // 6. Application service
    appSvc := app.NewService(userRepo, configRepo, sessionRepo, engine, twitchSvc, clock, 30*time.Second, 30*time.Second)

    // 7. Broadcaster
    onFirstClient := func(uuid uuid.UUID) { appSvc.IncrRefCount(ctx, uuid) }
    onSessionEmpty := func(uuid uuid.UUID) { appSvc.OnSessionEmpty(ctx, uuid) }
    broadcaster := broadcast.NewBroadcaster(engine, onFirstClient, onSessionEmpty, clock, 50, 50*time.Millisecond)

    // 8. HTTP server
    srv, _ := server.NewServer(cfg, appSvc, broadcaster, webhookHandler, pool, rdb)
    srv.Start()
}
```

### Constructor Pattern

All types use consistent constructor pattern:

```go
func NewService(dep1 Dep1, dep2 Dep2) *Service {
    return &Service{
        dep1: dep1,
        dep2: dep2,
    }
}
```

**No initialization logic in constructors** - just assignment.

## Alternatives Considered

### 1. google/wire - Compile-time DI

Codegen tool that generates wiring code.

```go
//go:build wireinject

func InitializeApp(cfg Config) (*App, error) {
    wire.Build(
        database.NewUserRepo,
        redis.NewClient,
        app.NewService,
    )
    return nil, nil
}
```

**Rejected because:**
- Generated code hard to debug (stack traces reference generated files)
- Extra build step (`go generate`)
- Overkill for 10-15 dependencies
- Magic (harder to understand where dependencies come from)

### 2. uber-go/dig - Runtime DI with Reflection

Container-based DI using reflection at runtime.

```go
container := dig.New()
container.Provide(database.NewUserRepo)
container.Provide(redis.NewClient)
container.Invoke(func(repo UserRepo) { ... })
```

**Rejected because:**
- Reflection overhead (negligible but non-zero)
- Magic (hard to trace where dependencies come from)
- IDE can't navigate ("Go to definition" broken)
- Runtime errors (missing providers found at runtime, not compile-time)

### 3. uber-go/fx - Opinionated DI Framework

Full application framework with lifecycle management.

```go
fx.New(
    fx.Provide(database.NewUserRepo),
    fx.Invoke(startServer),
).Run()
```

**Rejected because:**
- Heavy framework (changes app structure significantly)
- Magic lifecycle hooks (harder to reason about startup)
- Overkill for simple HTTP server
- Forces framework patterns (e.g., fx.Lifecycle)

## Consequences

### Positive

✅ **Explicit and visible** - Easy to trace dependency flow
✅ **Zero runtime overhead** - No reflection, no codegen
✅ **IDE-friendly** - "Go to definition" works perfectly
✅ **Simple** - No framework magic, just constructor calls
✅ **Compile-time safety** - Missing deps caught by compiler

### Negative

❌ **Verbose** - 20-30 lines of constructor calls
❌ **Boilerplate grows** - Each new dependency = new line
❌ **Manual ordering** - Must wire in correct dependency order
❌ **No cycle detection** - Circular deps not caught automatically

### Trade-offs

🔄 **Explicitness over brevity** - Accept boilerplate for clarity
🔄 **Simplicity over automation** - Manual wiring over codegen
🔄 **Debuggability over magic** - Clear call stack over reflection

## Testing Strategy

Tests use the same pattern - explicit constructor calls:

```go
func TestSomething(t *testing.T) {
    // Arrange - construct dependencies
    mockRepo := &mockUserRepo{}
    mockEngine := &mockEngine{}
    svc := app.NewService(mockRepo, nil, nil, mockEngine, nil, clock, 30*time.Second, 30*time.Second)

    // Act & Assert
    err := svc.SomeMethod()
    assert.NoError(t, err)
}
```

**Benefits for testing:**
- Easy to mock (pass interface implementations)
- Clear test setup (no hidden dependencies)
- Can construct partial dependency trees

## Dependency Graph

```
main.go
  ├─ pool (PostgreSQL)
  ├─ rdb (Redis)
  ├─ clock (clockwork.Clock)
  │
  ├─ cryptoSvc (crypto.Service)
  │
  ├─ Repositories (depend on pool/rdb)
  │  ├─ userRepo (pool, cryptoSvc)
  │  ├─ configRepo (pool)
  │  ├─ eventSubRepo (pool)
  │  ├─ sessionRepo (rdb, clock)
  │  ├─ sentimentStore (rdb)
  │  ├─ debouncer (rdb)
  │  └─ voteRateLimiter (rdb, clock, config)
  │
  ├─ Domain Services
  │  ├─ configCache (clock)
  │  └─ engine (sessionRepo, sentimentStore, debouncer, voteRateLimiter, clock, configCache)
  │
  ├─ Twitch (optional)
  │  └─ eventsubMgr (config, eventSubRepo)
  │
  ├─ appSvc (userRepo, configRepo, sessionRepo, engine, twitchSvc, clock, durations)
  ├─ broadcaster (engine, callbacks, clock, config)
  └─ server (cfg, appSvc, broadcaster, webhookHandler, pool, rdb)
```

**Leaf nodes:** Infrastructure (pool, rdb, clock)
**Root node:** HTTP server

## Related Decisions

- None (internal implementation choice)

## Implementation

**Files:**
- `cmd/server/main.go` - Manual DI wiring (lines 155-250)

**Commit:** Initial implementation (2025-12)

## Future Considerations

**If dependency count grows beyond 30:**
- Consider google/wire for codegen (still explicit, just automated)
- Keep manual DI for core dependencies, use wire for helpers

**If circular dependencies appear:**
- Redesign interfaces to break cycles
- Use dependency inversion (introduce interfaces)

## References

- [Go Proverbs: "A little copying is better than a little dependency"](https://go-proverbs.github.io/)
- [Effective Go: Package initialization](https://go.dev/doc/effective_go#init)
