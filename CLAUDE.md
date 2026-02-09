# CLAUDE.md

## Project Overview

ChatPulse - a Go application that tracks real-time chat sentiment for Twitch streamers via an OBS browser source overlay. Uses a dedicated bot account to read chat on behalf of all streamers. Designed as a multi-tenant SaaS supporting horizontal scaling across multiple instances via Redis.

## Build & Run

```bash
make build          # Build binary → ./server
make run            # Build and run locally
make test           # Run all tests (go test -v ./...)
make test-short     # Run unit tests only (go test -short ./...)
make test-race      # Run tests with race detector
make test-coverage  # Generate coverage.out and coverage.html
make fmt            # Format code (go fmt ./...)
make lint           # Run golangci-lint (configured via .golangci.yml)
make deps           # go mod download && go mod tidy
make docker-up      # Start with Docker Compose (app + PostgreSQL + Redis)
make docker-down    # Stop Docker Compose
make clean          # Remove build artifacts
```

Environment is loaded automatically from `.env` via `godotenv` (called inside `config.Load()`), then mapped to `Config` struct fields via `go-simpler/env` struct tags. See `.env.example` for required variables.

## Project Structure

```
cmd/server/main.go          Entry point (setupConfig, setupDB, setupTwitchClient, setupRedis, initWebhooks, runGracefulShutdown)
internal/
  domain/domain.go          All shared types (User, Config, ConfigSnapshot, EventSubSubscription, SessionUpdate) and cross-cutting interfaces (SessionStateStore, VoteStore, ScaleProvider, DataStore, AppService, TwitchService)
  config/config.go          Struct-tag-based env loading (go-simpler/env), .env file support (godotenv), validation
  database/postgres.go      PostgreSQL connection, migrations, CRUD, token encryption, health check
  app/service.go            Application layer: orchestrates session lifecycle, cleanup timer, config saves
  redis/
    client.go               Redis connection wrapper (NewClient, Ping, Close)
    session.go              Redis-backed session state (keys, debounce, orphan scanning)
    scripts.go              Lua scripts for atomic vote + decay operations
    store.go                SentimentStore: Redis-backed domain.SessionStateStore adapter
  sentiment/
    engine.go               Thin read-only wrapper: GetCurrentValue (time-decayed read via store + clock)
    trigger.go              Pure function: MatchTrigger for vote matching
    vote.go                 ProcessVote pipeline function (uses domain.VoteStore)
  server/
    server.go               Echo server setup, lifecycle
    routes.go               Route definitions (incl. webhook endpoint)
    handlers.go             Shared helpers (renderTemplate, getBaseURL, session constants)
    handlers_auth.go        Auth middleware, OAuth flow, login/logout
    handlers_dashboard.go   Dashboard page, config save, input validation
    handlers_api.go         REST API endpoints (reset sentiment, rotate overlay UUID)
    handlers_overlay.go     Overlay page + WebSocket upgrade
    oauth_client.go         twitchOAuthClient interface + HTTP implementation
  twitch/
    client.go               Kappopher-based Twitch API wrapper (conduit + EventSub CRUD)
    eventsub.go             EventSubManager: conduit lifecycle + DB-backed subscription management
    webhook.go              EventSub webhook handler (HMAC verification, direct-to-store voting)
  websocket/
    broadcaster.go          OverlayBroadcaster: actor pattern, pull-based tick loop, per-session client management
    writer.go               Per-connection write goroutine (clientWriter, buffered send, ping/pong heartbeat)
web/templates/
  login.html                Login page with Twitch OAuth button
  dashboard.html            Streamer config UI (auth required, has logout + rotate URL)
  overlay.html              OBS overlay (glassmorphism, status indicator)
```

## Key Routes

- `GET /` - Redirects to `/dashboard`
- `GET /auth/login` - Login page with Twitch OAuth button
- `GET /auth/callback` - Twitch OAuth callback
- `POST /auth/logout` - Clears session, redirects to login page
- `GET /dashboard` - Streamer config page (auth required)
- `POST /dashboard/config` - Save config (auth required, live-updates config)
- `POST /api/reset/:uuid` - Reset sentiment bar (auth required)
- `POST /api/rotate-overlay-uuid` - Generate new overlay URL (auth required)
- `GET /overlay/:uuid` - Serve overlay page (public, uses overlay UUID)
- `GET /ws/overlay/:uuid` - WebSocket endpoint (public, uses overlay UUID)
- `POST /webhooks/eventsub` - Twitch EventSub webhook receiver (when configured)

## Environment Variables

All defined in `.env.example`.

**Required**: `DATABASE_URL`, `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET`, `TWITCH_REDIRECT_URI`, `SESSION_SECRET`, `REDIS_URL`.

**Webhook group** (all three required together):
- `WEBHOOK_CALLBACK_URL` — Public HTTPS URL for EventSub webhook delivery (required for production)
- `WEBHOOK_SECRET` — HMAC secret for webhook signature verification (10-100 chars)
- `BOT_USER_ID` — Twitch user ID of the dedicated bot account that reads chat

**Optional**:
- `TOKEN_ENCRYPTION_KEY` — 64 hex chars (32 bytes) for AES-256-GCM token encryption at rest; if empty, tokens stored in plaintext

## Database

PostgreSQL 15+. Three tables auto-created via migrations in `postgres.go`:
- `users` — Twitch OAuth data, tokens (encrypted at rest if `TOKEN_ENCRYPTION_KEY` set), expiry, `overlay_uuid` (separate from internal `id`)
- `configs` — Per-user sentiment config (triggers, labels, decay speed)
- `eventsub_subscriptions` — Tracks active EventSub subscriptions per user (user_id FK, broadcaster_user_id, subscription_id, conduit_id)

## Architecture Notes

### Scaling Model

Redis-only architecture. Session state lives in Redis, Lua scripts handle atomic vote/decay operations. Multiple instances share state via Redis; ref counting (`IncrRefCount`/`DecrRefCount`) tracks how many instances serve each session. The `OverlayBroadcaster` pulls current values from Redis on each tick (50ms) rather than subscribing to push notifications.

### Bot Account Architecture

A single dedicated bot account reads chat on behalf of all streamers:

- **Bot account** authorizes `user:read:chat` + `user:bot` scopes for the app (one-time setup)
- **Streamers** only grant `channel:bot` scope during OAuth login, allowing the bot into their channel
- EventSub subscriptions set `user_id` to the bot's Twitch user ID and `broadcaster_user_id` to the streamer
- `BOT_USER_ID` env var provides the bot's Twitch user ID; stored in `EventSubManager` and threaded to `CreateEventSubSubscription`

### EventSub Transport: Webhooks + Conduits

Chat messages are received via **Twitch EventSub webhooks** transported through a **Conduit**:

- **EventSub Manager** (`twitch/eventsub.go`): Manages both conduit lifecycle and EventSub subscriptions. On startup (`Setup`), finds an existing conduit via `GetConduits()` or creates a new one, then configures a webhook shard pointing to the app's `WEBHOOK_CALLBACK_URL`. The `conduitID` is internal — no longer passed between separate managers. Creates/deletes EventSub subscriptions targeting the conduit. Stores `botUserID` and passes it to `CreateEventSubSubscription`. Handles Twitch 409 Conflict via `helix.APIError` type assertion (subscription already exists) as idempotent success. Subscriptions are persisted in PostgreSQL (`eventsub_subscriptions` table) for idempotency and crash recovery. On subscribe failure after Twitch API success, attempts cleanup. On shutdown (`Cleanup`), deletes the conduit.
- **Webhook Handler** (`twitch/webhook.go`): Receives Twitch POST notifications with Kappopher's built-in HMAC-SHA256 signature verification. Processes votes via `sentiment.ProcessVote()` through the `VoteStore` interface, bypassing the Engine actor for minimal latency. Flow: broadcaster lookup → trigger match → debounce check → atomic vote application.

### Concurrency Model: Actor Pattern

The `OverlayBroadcaster` (`websocket/broadcaster.go`) uses the **actor pattern** — a single goroutine owns all mutable state and receives typed commands via a buffered channel. No mutexes on actor-owned state.

The actor follows this shape:
- Buffered command channel for typed commands (fire-and-forget or request/reply via embedded reply channel)
- Single `run()` goroutine with `select` on commands and ticker, `default` case logging unknown command types
- `Stop()` method for clean shutdown
- Command types use an embedded `baseBroadcasterCmd` struct to satisfy the marker interface

### OverlayBroadcaster (`websocket/broadcaster.go`)

- Actor goroutine owns the `activeClients` map (session UUID → set of `*clientWriter`). `NewOverlayBroadcaster` accepts a `domain.ScaleProvider` (the Engine), an `onSessionEmpty` callback, and a `clockwork.Clock`.
- **Pull-based broadcasting**: A 50ms ticker calls `engine.GetCurrentValue()` for each active session and broadcasts `domain.SessionUpdate{Value, Status: "active"}` to all connected clients. JSON marshaling happens once per session before fanning out.
- **Per-connection write goroutines** (`clientWriter` in `writer.go`): each WebSocket connection gets its own goroutine with a buffered send channel (cap 16), 5-second write deadlines, and periodic ping/pong heartbeat (30s ping interval, 60s pong deadline). Slow clients are disconnected (non-blocking send) instead of blocking all broadcasts.
- **Per-session client cap**: `maxClientsPerSession = 50` prevents resource exhaustion.
- **Session empty callback**: When the last client disconnects from a session, the `onSessionEmpty` callback fires (wired to `app.Service.OnSessionEmpty` in main.go), which decrements the Redis ref count and marks the session as disconnected if no instances serve it.

### Sentiment Engine (`sentiment/engine.go`)

- Thin read-only wrapper — no actor, no goroutines. All mutable state lives in Redis.
- `GetCurrentValue(ctx, sessionUUID)` reads the session config, then calls `store.GetDecayedValue()` with the current clock time. The Lua script in Redis computes the time-decayed value atomically.
- Implements `domain.ScaleProvider`, consumed by the `OverlayBroadcaster`'s tick loop.

### SessionStateStore Interface (`domain/domain.go`)

Abstracts session state (16 methods including `IncrRefCount`/`DecrRefCount` for multi-instance ref counting). Single implementation:

- **`SentimentStore`** (`redis/store.go`): Wraps `redis.SessionStore` and `redis.ScriptRunner`. Accepts `clockwork.Clock` for consistent time handling. Debounce via `SETNX` with 1s TTL (auto-expires, no pruning needed). Vote/decay via Lua scripts (atomic operations). Lives in the `redis` package alongside the infrastructure it wraps, keeping `sentiment/` free of infrastructure imports.

### Vote Processing Pipeline

Votes flow through the `sentiment.ProcessVote()` function, which encapsulates the full pipeline via the `domain.VoteStore` interface (4 methods). The webhook handler calls this function directly for minimal latency. The bot account's `user_id` in the EventSub subscription means Twitch sends all chat messages from channels the bot has joined:

1. Twitch sends `channel.chat.message` webhook → Kappopher verifies HMAC
2. `store.GetSessionByBroadcaster(broadcasterUserID)` → session UUID
3. `store.GetSessionConfig(sessionUUID)` → config
4. `sentiment.MatchTrigger(messageText, config)` → delta (+10, -10, or 0)
5. `store.CheckDebounce(sessionUUID, chatterUserID)` → allowed (SETNX in Redis)
6. `store.ApplyVote(sessionUUID, delta)` → new value (Lua script: clamp + PUBLISH)

Step 6's Lua script atomically applies the time-decayed vote in Redis. The `OverlayBroadcaster`'s next tick (≤50ms later) reads the updated value via `Engine.GetCurrentValue` and broadcasts to local WebSocket clients.

### Redis Architecture (`internal/redis/`)

**Key schema**:
- `session:{overlayUUID}` — hash: `value`, `broadcaster_user_id`, `config_json`, `last_disconnect`, `last_decay`
- `broadcaster:{twitchUserID}` — string → overlayUUID
- `debounce:{overlayUUID}:{twitchUserID}` — key with 1s TTL (auto-expires)

**Lua scripts** (run atomically server-side in Redis):
- `applyVoteScript`: `HGET value + last_decay → apply time-decay → clamp(decayed + delta, -100, 100) → HSET value + last_decay`
- `getDecayedValueScript`: `HGET value + last_decay → compute time-decayed value → return` (read-only, used by Engine's `GetCurrentValue`)

### Twitch Client (`twitch/client.go`)

Wraps Kappopher client for Twitch API operations. `NewTwitchClient(clientID, clientSecret)` creates the client.
- **App-scoped client**: Uses client credentials flow for conduit management and EventSub CRUD
- **Conduit operations**: `CreateConduit`, `GetConduits`, `UpdateConduitShards`, `DeleteConduit`
- **EventSub operations**: `CreateEventSubSubscription` (conduit transport, accepts `botUserID` param), `DeleteEventSubSubscription`
- **`TokenRefreshError`**: Error type for token refresh failures with `Revoked` flag

### HTTP Server (`internal/server/`)

- **Echo Framework**: Uses Echo v4 with Logger and Recover middleware. Sessions via gorilla/sessions with `sessionMaxAgeDays` (7) expiry, secure cookies in production. Templates are parsed once at startup and cached in the `Server` struct. `NewServer` returns `(*Server, error)`.
- **Handler File Organization**: Handlers are split by domain — `handlers_auth.go` (auth middleware, OAuth, login/logout), `handlers_dashboard.go` (dashboard page, config save, validation), `handlers_api.go` (REST API), `handlers_overlay.go` (overlay page, WebSocket). Shared helpers (`renderTemplate`, `getBaseURL`, session constants) live in `handlers.go`.
- **Auth Middleware** (`requireAuth` in `handlers_auth.go`): Checks session for user ID, parses UUID, stores in Echo context. Redirects to `/auth/login` if not authenticated.
- **OAuth Flow**: Uses the `twitchOAuthClient` interface (mockable for tests). `GET /auth/login` → generate CSRF state + store in session → Twitch OAuth (scope: `channel:bot`, `state` param) → `/auth/callback` → verify state matches session → `oauthClient.ExchangeCodeForToken()` → `twitchTokenResult` → upsert to DB → create session → `/dashboard`.
- **WebSocket Lifecycle** (`handlers_overlay.go`): `GET /ws/overlay/:uuid` → parse overlay UUID → lookup user → `app.EnsureSessionActive` → `app.IncrRefCount` → upgrade to WebSocket → `broadcaster.Register` → read pump blocks → `broadcaster.Unregister` → `app.OnSessionEmpty`.
- **Graceful Shutdown**: `SIGINT`/`SIGTERM` → `runGracefulShutdown` returns a `done` channel → `server.Shutdown` → `appSvc.Stop` (stops cleanup timer) → `broadcaster.Stop` → `eventsubManager.Cleanup` → `close(done)`. Main goroutine waits on `<-done` after `srv.Start()` returns, ensuring all deferred cleanup (DB close, Redis close) executes properly.

### Application Layer (`app/service.go`)

- Orchestrates all use cases: session activation, ref counting, config saves, overlay UUID rotation, orphan cleanup.
- `EnsureSessionActive`: Uses `singleflight` to collapse concurrent activations. Checks if session exists in Redis; if not, loads user + config from DB, activates in store, and subscribes via Twitch EventSub.
- `OnSessionEmpty`: Decrements Redis ref count; marks session as disconnected when ref count reaches 0 (no instances serving it).
- **Cleanup Timer**: 30s ticker calls `CleanupOrphans`, which lists sessions disconnected for >30s, deletes them from Redis, and fires Twitch unsubscribe in a background goroutine.
- Accepts `domain.DataStore`, `domain.SessionStateStore`, and `domain.TwitchService` (nil-safe for the Twitch dependency when webhooks are not configured).

### Domain Package (`domain/domain.go`)

Single file containing all shared types and cross-cutting interfaces:
- **Model types**: `User`, `Config`, `ConfigSnapshot`, `EventSubSubscription`, `SessionUpdate`
- **Interfaces**: `SessionStateStore` (16 methods), `VoteStore` (4 methods), `ScaleProvider` (1 method), `DataStore` (6 methods), `AppService` (6 methods), `TwitchService` (2 methods)
- Imports only stdlib + `github.com/google/uuid` — no internal dependencies, preventing circular imports.

### Other Architecture Notes

- **Trigger Matching**: `sentiment.MatchTrigger()` — pure function, case-insensitive substring match. "For" trigger takes priority when both match. Returns +`voteDelta`, -`voteDelta`, or 0 (`voteDelta` = 10.0).
- **Vote Clamping**: Values clamped to [-100, 100].
- **Debounce**: 1 second per user per session. Redis `SETNX` with 1s TTL (auto-expires, no pruning needed).
- **Overlay UUID Separation**: The overlay URL uses a separate `overlay_uuid` column (not the user's internal `id`). Users can rotate their overlay UUID via `POST /api/rotate-overlay-uuid` to invalidate old URLs.
- **Token Encryption at Rest**: AES-256-GCM encryption for access/refresh tokens in PostgreSQL. `Connect(databaseURL, encryptionKeyHex)` accepts a 32-byte hex key. If key is empty, tokens pass through unencrypted (dev/test mode). The GCM cipher is created once in `Connect()` and cached on the `DB` struct for reuse. Nonce prepended to ciphertext, hex-encoded for TEXT column storage.
- **UpsertUser Transaction**: User creation and default config insertion are wrapped in a single database transaction for atomicity. Uses `scanUser()` helper for consistent row scanning + token decryption.
- **Connection Status Broadcasting**: Broadcaster sends `{"value": float, "status": "active"}` on each tick. Overlay shows "reconnecting..." indicator when status !== "active".
- **Health Check**: `db.HealthCheck(ctx)` calls `PingContext` for readiness probes.
- **DB Connection Pool**: `MaxOpenConns(25)`, `MaxIdleConns(5)`, `ConnMaxLifetime(5min)`.
- **Context Propagation**: All DB queries and HTTP calls accept `context.Context` as first parameter for cancellation support.

## Testability Interfaces

The codebase uses consumer-side interfaces for testability. Cross-cutting interfaces live in `domain/domain.go`; package-private interfaces stay with their single consumer.

**Domain interfaces** (`domain/domain.go`):
- **`domain.SessionStateStore`** — abstracts session state (16 methods). Implemented by `redis.SentimentStore` (with `clockwork.Clock` injection).
- **`domain.VoteStore`** — narrow 4-method interface (ISP-clean) for the vote processing pipeline: `GetSessionByBroadcaster`, `GetSessionConfig`, `CheckDebounce`, `ApplyVote`. `SessionStateStore` implementations satisfy it.
- **`domain.ScaleProvider`** — `GetCurrentValue(ctx, uuid) (float64, error)`. Implemented by `sentiment.Engine`. Consumed by `websocket.OverlayBroadcaster`.
- **`domain.DataStore`** — 6-method subset of `*database.DB` used by both server and app layers (`GetUserByID`, `GetUserByOverlayUUID`, `GetConfig`, `UpsertUser`, `UpdateConfig`, `RotateOverlayUUID`).
- **`domain.AppService`** — 6-method contract between server and app layer (`EnsureSessionActive`, `IncrRefCount`, `OnSessionEmpty`, `ResetSentiment`, `SaveConfig`, `RotateOverlayUUID`). Implemented by `app.Service`.
- **`domain.TwitchService`** — `Subscribe`, `Unsubscribe`. Implemented by `twitch.EventSubManager`.

**Package-private interfaces** (single consumer, stay local):
- **`server.twitchOAuthClient`** — `ExchangeCodeForToken(ctx, code) (*twitchTokenResult, error)`. Production implementation wraps Twitch HTTP APIs; tests use `mockOAuthClient`.
- **`server.webhookHandler`** — `HandleEventSub(c echo.Context) error`. Nil when webhooks not configured.
- **`twitch.eventsubAPIClient`** — subset of `TwitchClient` used by `EventSubManager` (6 methods: `CreateConduit`, `GetConduits`, `UpdateConduitShards`, `DeleteConduit`, `CreateEventSubSubscription`, `DeleteEventSubSubscription`).
- **`twitch.subscriptionStore`** — subset of `*database.DB` used by `EventSubManager`.
- **`clockwork.Clock`** — injected into `sentiment.Engine`, `redis.SentimentStore`, `app.Service`, and `websocket.OverlayBroadcaster` (threaded to `clientWriter` for write deadlines, ping tickers, and pong read deadlines) for deterministic time control in tests.

### Testing with the Actor Pattern

The `OverlayBroadcaster` uses an actor pattern. Tests use synchronous queries (e.g., `GetClientCount()`) as **barrier calls** — the reply won't come until all prior commands in the channel are processed. For `CleanupOrphans` (in `app.Service`), Twitch unsubscribe calls run in a background goroutine — tests use a channel with timeout to wait for completion.

## Testing

130 tests across 7 packages. Run with `make test` or `go test ./...`.

### Test Types

**Unit tests** (fast, no external dependencies):
- Run with `make test-short` or `go test -short ./...`
- Complete in <2 seconds
- Use mocks for all external dependencies

**Integration tests** (use real infrastructure):
- Run with `make test` or `go test ./...`
- Complete in ~15 seconds
- Use testcontainers for PostgreSQL and Redis, real WebSockets, mock HTTP servers for external APIs
- Automatically skipped with `-short` flag via `if testing.Short() { t.Skip(...) }`

**Coverage analysis**:
- Run with `make test-coverage` to generate `coverage.out` and `coverage.html`
- Race detection: `make test-race` (runs tests with `-race`)

### Test Files

- `internal/config/config_test.go` — env loading, defaults, validation, encryption key validation, webhook config validation
- `internal/database/postgres_test.go` — CRUD operations, token encryption, migrations, health check, eventsub subscriptions (testcontainers)
- `internal/database/testhelpers.go` — test fixtures for creating users and configs
- `internal/redis/redis_test.go` — test setup: testcontainers Redis, `setupTestClient` helper
- `internal/redis/session_test.go` — activate, resume, delete, broadcaster mapping, debounce, orphan listing (18 tests)
- `internal/redis/pubsub_test.go` — publish/subscribe roundtrip, multi-message, session isolation, close (4 tests)
- `internal/redis/scripts_test.go` — Lua script vote application, clamping, decay with timestamp guard (3 tests)
- `internal/sentiment/engine_test.go` — GetCurrentValue: decayed value, no session, clock time, decay math (4 tests)
- `internal/sentiment/trigger_test.go` — MatchTrigger: for/against, case-insensitive, priority, nil config, empty message (7 tests)
- `internal/app/service_test.go` — EnsureSessionActive, OnSessionEmpty, IncrRefCount, ResetSentiment, SaveConfig, RotateOverlayUUID, CleanupOrphans, Stop (14 tests)
- `internal/websocket/broadcaster_test.go` — register, tick broadcast, multiple clients, session empty callback, client cap, value updates (7 tests)
- `internal/server/handlers_test.go` — shared test infrastructure: mocks (`mockDataStore`, `mockAppService`, `mockOAuthClient`), helpers (`newTestServer`, `setSessionUserID`, `withOAuthClient`)
- `internal/server/handlers_unit_test.go` — input validation tests (validateConfig: 100% coverage)
- `internal/server/handlers_auth_test.go` — requireAuth middleware, login page, logout, OAuth callback (success, missing code, invalid state, exchange error, DB error) (10 tests)
- `internal/server/handlers_dashboard_test.go` — handleDashboard, handleSaveConfig (5 tests)
- `internal/server/handlers_api_test.go` — handleResetSentiment, handleRotateOverlayUUID (5 tests)
- `internal/server/handlers_overlay_test.go` — handleOverlay (3 tests)
- `internal/twitch/client_test.go` — TokenRefreshError formatting
- `internal/twitch/webhook_test.go` — webhook handler tests with HMAC-signed requests: trigger matching, debounce, invalid signature, non-chat events (5 tests)

### Test Dependencies

- **`stretchr/testify`** — assertions and require helpers
- **`jonboulle/clockwork`** — fake clock for deterministic time control
- **`testcontainers-go`** — container orchestration (v0.40.0)
- **`testcontainers-go/modules/postgres`** — PostgreSQL module
- **`testcontainers-go/modules/redis`** — Redis module

### Database Integration Tests (testcontainers)

**Setup**: `TestMain()` starts a PostgreSQL 15 container once, reused across all tests (~2-5s overhead total)
**Cleanup**: Each test uses `setupTestDB(t)` which registers a cleanup function to `TRUNCATE users, configs CASCADE`
**Production fidelity**: Real PostgreSQL behavior, real schema, real constraints, real encryption

### Redis Integration Tests (testcontainers)

**Setup**: `TestMain()` starts a Redis 7 container once, reused across all tests (~1-2s overhead)
**Cleanup**: Each test calls `FlushAll` before running
**Tests cover**: Session CRUD, broadcaster mapping, debounce TTL, orphan scanning, Pub/Sub roundtrip, Lua script atomicity (vote clamping, decay timestamp guard)

### Server Validation Tests (unit)

**Input Validation** (`internal/server/handlers_unit_test.go`):
- Empty trigger detection (for/against), length limits (triggers: 500, labels: 50), decay range (0.1-2.0), identical trigger prevention

## Linting

golangci-lint v2 configured via `.golangci.yml` (schema `version: "2"`). Run with `make lint`.

**Enabled linters** (beyond the `standard` default set): bodyclose, durationcheck, errorlint, fatcontext, goconst, gocritic (diagnostic + performance tags), misspell, nilerr, noctx, prealloc, unconvert, unparam, wastedassign.

**Suppressed patterns** (intentional, not bugs):
- `errcheck` on `Close()`, `SetWriteDeadline()`, `SetReadDeadline()`, `SetPongHandler()` for standard cleanup/setup patterns
- `exitAfterDefer` — standard Go pattern in `main()` and `TestMain()`
- `middleware.Logger` deprecation — replacing requires larger refactor
- `bodyclose` in test files — false positive on WebSocket `Dial()`

## Go Module

Module: `github.com/pscheid92/chatpulse`, Go 1.25.7.

Key deps: echo/v4, gorilla/websocket, gorilla/sessions, Its-donkey/kappopher, lib/pq, redis/go-redis/v9, google/uuid, joho/godotenv, jonboulle/clockwork, go-simpler/env.

Test deps: stretchr/testify, jonboulle/clockwork, testcontainers-go, testcontainers-go/modules/postgres, testcontainers-go/modules/redis.

### Dependency Changes (Feb 2026 Architecture Overhaul)

| Removed | Added |
|---------|-------|
| `github.com/nicklaw5/helix/v2` | `github.com/Its-donkey/kappopher` v1.1.1 |
| | `github.com/redis/go-redis/v9` v9.17.3 |
| | `github.com/testcontainers/testcontainers-go/modules/redis` v0.40.0 |
| | `go-simpler.org/env` v0.12.0 |

### Files Deleted / Renamed

- `internal/twitch/eventsub.go` — 660-line EventSub WebSocket actor with 5-state FSM (replaced by webhooks + conduits)
- `internal/twitch/token.go` — Token refresh middleware (replaced by Kappopher's built-in token management)
- `internal/twitch/token_test.go` — Tests for deleted token code
- `internal/twitch/helix.go` → `internal/twitch/client.go` — Renamed `HelixClient` to `TwitchClient`, deleted unused `GetConduitShards`, `GetUsers`, `AppAuthClient` methods
- `internal/twitch/helix_test.go` → `internal/twitch/client_test.go` — Renamed alongside client
- `internal/sentiment/store_redis.go` → `internal/redis/store.go` — Moved Redis `SessionStateStore` adapter out of domain package; renamed `RedisStore` to `SentimentStore` to avoid `redis.RedisStore` stutter
- `internal/models/user.go` — Model types (`User`, `Config`, `ConfigSnapshot`, `EventSubSubscription`) moved to `internal/domain/domain.go`; `models` package deleted
- `internal/sentiment/store.go` — `SessionStateStore`, `VoteStore`, `SessionUpdate` moved to `internal/domain/domain.go`
- `internal/sentiment/store_memory.go` — In-memory store removed (Redis-only architecture)
- `internal/websocket/hub.go` → `internal/websocket/broadcaster.go` — Replaced push-based Hub with pull-based `OverlayBroadcaster`
- `internal/websocket/commands.go` — Hub command types removed (broadcaster defines its own internally)
- `internal/redis/pubsub.go` — Pub/Sub removed (broadcaster pulls values instead of subscribing)
- `internal/twitch/conduit.go` + `internal/twitch/subscription.go` → `internal/twitch/eventsub.go` — Merged `ConduitManager` and `SubscriptionManager` into single `EventSubManager`; `conduitID` internalized
