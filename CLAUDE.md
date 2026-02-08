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

Environment is loaded automatically via `godotenv` from `.env` (see `.env.example` for required variables).

## Project Structure

```
cmd/server/main.go          Entry point with helper functions (initStore, initWebhooks, newHubCallbacks, runGracefulShutdown)
internal/
  config/config.go          Environment variable loading + validation
  database/postgres.go      PostgreSQL connection, migrations, CRUD, token encryption, health check
  models/user.go            User, Config, ConfigSnapshot, EventSubSubscription models
  redis/
    client.go               Redis connection wrapper (NewClient, Ping, Close)
    session.go              Redis-backed session state (keys, debounce, orphan scanning)
    pubsub.go               Cross-instance broadcast via Redis Pub/Sub
    scripts.go              Lua scripts for atomic vote + decay operations
  sentiment/
    engine.go               Core engine: actor pattern, tick/decay, session lifecycle
    store.go                SessionStateStore + DebouncePruner interface definitions
    store_memory.go         In-memory store (single-instance mode)
    store_redis.go          Redis store (multi-instance mode)
    trigger.go              Pure function: MatchTrigger for vote matching
    vote.go                 VoteStore interface + ProcessVote pipeline function
  server/
    server.go               Echo server setup, lifecycle, cleanup sweep
    routes.go               Route definitions (incl. webhook endpoint)
    handlers.go             Shared helpers (renderTemplate, getBaseURL, session constants)
    handlers_auth.go        Auth middleware, OAuth flow, login/logout
    handlers_dashboard.go   Dashboard page, config save, input validation
    handlers_api.go         REST API endpoints (reset sentiment, rotate overlay UUID)
    handlers_overlay.go     Overlay page + WebSocket upgrade
    oauth_client.go         twitchOAuthClient interface + HTTP implementation
  twitch/
    client.go               Kappopher-based Twitch API wrapper (conduit + EventSub CRUD)
    conduit.go              Conduit lifecycle (create/find/configure webhook shard/cleanup)
    webhook.go              EventSub webhook handler (HMAC verification, direct-to-store voting)
    subscription.go         DB-backed EventSub subscription manager
  websocket/hub.go          WebSocket hub (actor pattern, per-conn writers, Redis Pub/Sub listener)
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

**Required**: `DATABASE_URL`, `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET`, `TWITCH_REDIRECT_URI`, `SESSION_SECRET`.

**Webhook group** (all three required together):
- `WEBHOOK_CALLBACK_URL` — Public HTTPS URL for EventSub webhook delivery (required for production)
- `WEBHOOK_SECRET` — HMAC secret for webhook signature verification (10-100 chars)
- `BOT_USER_ID` — Twitch user ID of the dedicated bot account that reads chat

**Optional**:
- `TOKEN_ENCRYPTION_KEY` — 64 hex chars (32 bytes) for AES-256-GCM token encryption at rest; if empty, tokens stored in plaintext
- `REDIS_URL` — Redis connection URL (e.g., `redis://localhost:6379`); enables multi-instance mode

## Database

PostgreSQL 15+. Three tables auto-created via migrations in `postgres.go`:
- `users` — Twitch OAuth data, tokens (encrypted at rest if `TOKEN_ENCRYPTION_KEY` set), expiry, `overlay_uuid` (separate from internal `id`)
- `configs` — Per-user sentiment config (triggers, labels, decay speed)
- `eventsub_subscriptions` — Tracks active EventSub subscriptions per user (user_id FK, broadcaster_user_id, subscription_id, conduit_id)

## Architecture Notes

### Scaling Model

The app supports two deployment modes:

1. **Single-instance** (no `REDIS_URL`): In-memory session state, Engine broadcasts directly to Hub. Good for development and small deployments.
2. **Multi-instance** (`REDIS_URL` set): Session state in Redis, Lua scripts for atomic vote/decay, Redis Pub/Sub for cross-instance broadcasting. Hub subscribes to Pub/Sub channels for each active session.

### Bot Account Architecture

A single dedicated bot account reads chat on behalf of all streamers:

- **Bot account** authorizes `user:read:chat` + `user:bot` scopes for the app (one-time setup)
- **Streamers** only grant `channel:bot` scope during OAuth login, allowing the bot into their channel
- EventSub subscriptions set `user_id` to the bot's Twitch user ID and `broadcaster_user_id` to the streamer
- `BOT_USER_ID` env var provides the bot's Twitch user ID; stored in `SubscriptionManager` and threaded to `CreateEventSubSubscription`

### EventSub Transport: Webhooks + Conduits

Chat messages are received via **Twitch EventSub webhooks** transported through a **Conduit**:

- **Conduit Manager** (`twitch/conduit.go`): On startup, finds an existing conduit via `GetConduits()` or creates a new one, then configures a webhook shard pointing to the app's `WEBHOOK_CALLBACK_URL`. On shutdown, deletes the conduit.
- **Webhook Handler** (`twitch/webhook.go`): Receives Twitch POST notifications with Kappopher's built-in HMAC-SHA256 signature verification. Processes votes via `sentiment.ProcessVote()` through the `VoteStore` interface, bypassing the Engine actor for minimal latency. Flow: broadcaster lookup → trigger match → debounce check → atomic vote application.
- **Subscription Manager** (`twitch/subscription.go`): Creates/deletes EventSub subscriptions targeting the conduit. Stores `botUserID` and passes it to `CreateEventSubSubscription`. Handles Twitch 409 Conflict via `helix.APIError` type assertion (subscription already exists) as idempotent success. Subscriptions are persisted in PostgreSQL (`eventsub_subscriptions` table) for idempotency and crash recovery. On subscribe failure after Twitch API success, attempts cleanup.

### Concurrency Model: Actor Pattern

Two core components use the **actor pattern** — a single goroutine owns all mutable state and receives typed commands via a buffered channel. No mutexes on actor-owned state.

Each actor follows the same shape:
- Buffered `cmdCh` channel for commands (fire-and-forget or request/reply via embedded `replyCh`)
- Single `run()` goroutine with `for cmd := range cmdCh` loop
- `Stop()` method for clean shutdown

### WebSocket Hub (`websocket/hub.go`)

- Actor goroutine owns the `clients`, `pendingClients`, and `subscriptions` maps.
- **Per-connection write goroutines** (`clientWriter`): each WebSocket connection gets its own goroutine with a buffered send channel (cap 16) and 5-second write deadlines. Slow clients are disconnected (non-blocking send) instead of blocking all broadcasts.
- **Async `onFirstConnect`**: registration is two-phase — the callback runs in a background goroutine while clients are queued in `pendingClients`. When the callback completes, `cmdFirstConnectResult` promotes or rejects all queued clients.
- **Per-session client cap**: `maxClientsPerSession = 50` prevents resource exhaustion.
- **Redis Pub/Sub integration**: When `SetPubSub` is called, the Hub subscribes to Redis channels for each active session (`subscribePubSub`). A background goroutine per subscription reads updates and sends `cmdBroadcast` to the Hub's command channel. Subscriptions are closed on last client disconnect (`unsubscribePubSub`).

### Sentiment Engine (`sentiment/engine.go`)

- Actor goroutine delegates state operations to a **`SessionStateStore`** (in-memory or Redis).
- **`localSessions`** map tracks sessions activated on this instance (UUID → cached `ConfigSnapshot`). Used by the tick handler for decay factor calculation.
- **DB calls happen outside the actor**: `ActivateSession` checks if session exists (fast actor query via store), does the DB call in the caller's goroutine, then sends the config to the actor.
- **Ticker goroutine** sends `cmdTick` every 50ms. Actor calls `store.ApplyDecay()` for each local session. Broadcasting mode is determined by the `broadcastAfterDecay` flag (set at construction): in-memory mode broadcasts after each decay tick; Redis mode relies on Lua script PUBLISH via Pub/Sub.
- **Debounce pruning**: every ~50 seconds, if the store implements the optional `DebouncePruner` interface, `PruneDebounce()` is called (in-memory: prunes stale entries; Redis: no-op, TTL handles expiry).
- `CleanupOrphans` queries `store.ListOrphans()`, deletes orphan sessions from store + local tracking, fires Twitch unsubscribe calls in a background goroutine.

### SessionStateStore Interface (`sentiment/store.go`)

Abstracts session state (14 methods) with two implementations:

- **`InMemoryStore`** (`store_memory.go`): Maps for sessions, broadcaster→session mapping (with reverse mapping for O(1) deletion), and debounce entries. Accepts `clockwork.Clock` for test determinism. Also implements the optional `DebouncePruner` interface.
- **`RedisStore`** (`store_redis.go`): Wraps `redis.SessionStore` and `redis.ScriptRunner`. Accepts `clockwork.Clock` for consistent time handling. Debounce via `SETNX` with 1s TTL (auto-expires, no pruning needed). Vote/decay via Lua scripts (atomic update + PUBLISH).

### Vote Processing Pipeline

Votes flow through the `sentiment.ProcessVote()` function, which encapsulates the full pipeline via the `VoteStore` interface (4 methods). Both the webhook handler and the Engine actor use this same function, eliminating duplication. The bot account's `user_id` in the EventSub subscription means Twitch sends all chat messages from channels the bot has joined:

1. Twitch sends `channel.chat.message` webhook → Kappopher verifies HMAC
2. `store.GetSessionByBroadcaster(broadcasterUserID)` → session UUID
3. `store.GetSessionConfig(sessionUUID)` → config
4. `sentiment.MatchTrigger(messageText, config)` → delta (+10, -10, or 0)
5. `store.CheckDebounce(sessionUUID, chatterUserID)` → allowed (SETNX in Redis)
6. `store.ApplyVote(sessionUUID, delta)` → new value (Lua script: clamp + PUBLISH)

In Redis mode, step 6's Lua script publishes to `sentiment:{uuid}`, which the Hub's Pub/Sub listener receives and broadcasts to local WebSocket clients.

### Redis Architecture (`internal/redis/`)

**Key schema**:
- `session:{overlayUUID}` — hash: `value`, `broadcaster_user_id`, `config_json`, `last_disconnect`, `last_decay`
- `broadcaster:{twitchUserID}` — string → overlayUUID
- `debounce:{overlayUUID}:{twitchUserID}` — key with 1s TTL (auto-expires)

**Pub/Sub channels**: `sentiment:{overlayUUID}` — JSON `{value, status}`

**Lua scripts** (run atomically server-side in Redis):
- `applyVoteScript`: `HGET value → clamp(current + delta, -100, 100) → HSET → PUBLISH`
- `applyDecayScript`: `HGET value + last_decay → timestamp guard (min interval) → multiply by factor → HSET value + last_decay → PUBLISH`. The timestamp guard (40ms minimum interval) prevents double-decay across multiple instances.

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
- **WebSocket Lifecycle** (`handlers_overlay.go`): `GET /ws/overlay/:uuid` → parse overlay UUID → lookup user → upgrade to WebSocket → Hub `Register` → `onFirstConnect` callback activates session + subscribes EventSub → read pump blocks → `Unregister` → `MarkDisconnected`.
- **Lifecycle Callbacks**: Hub's `onFirstConnect` callback (wired in main.go) calls `ActivateSession` + `Subscribe`. Hub's `onLastDisconnect` callback calls `MarkDisconnected`. This decouples the Hub from domain logic.
- **Cleanup Timer**: Server starts a 30s ticker that calls `sentiment.CleanupOrphans(twitch)` to remove sessions with no clients for >30s.
- **Graceful Shutdown**: `SIGINT`/`SIGTERM` → `runGracefulShutdown` returns a `done` channel → `server.Shutdown` → stop cleanup timer → `echo.Shutdown` → `engine.Stop` → `hub.Stop` → `conduitManager.Cleanup` → `close(done)`. Main goroutine waits on `<-done` after `srv.Start()` returns, ensuring all deferred cleanup (DB close, Redis close) executes properly.

### Other Architecture Notes

- **Trigger Matching**: `sentiment.MatchTrigger()` — pure function, case-insensitive substring match. "For" trigger takes priority when both match. Returns +`voteDelta`, -`voteDelta`, or 0 (`voteDelta` = 10.0).
- **Vote Clamping**: Values clamped to [-100, 100].
- **Debounce**: 1 second per user per session. In-memory: tracked in a map with clock injection for tests. Redis: `SETNX` with 1s TTL (auto-expires, no pruning needed).
- **Overlay UUID Separation**: The overlay URL uses a separate `overlay_uuid` column (not the user's internal `id`). Users can rotate their overlay UUID via `POST /api/rotate-overlay-uuid` to invalidate old URLs.
- **Token Encryption at Rest**: AES-256-GCM encryption for access/refresh tokens in PostgreSQL. `Connect(databaseURL, encryptionKeyHex)` accepts a 32-byte hex key. If key is empty, tokens pass through unencrypted (dev/test mode). The GCM cipher is created once in `Connect()` and cached on the `DB` struct for reuse. Nonce prepended to ciphertext, hex-encoded for TEXT column storage.
- **UpsertUser Transaction**: User creation and default config insertion are wrapped in a single database transaction for atomicity. Uses `scanUser()` helper for consistent row scanning + token decryption.
- **Connection Status Broadcasting**: Engine broadcasts `{"value": float, "status": "active"}` via Hub. Overlay shows "reconnecting..." indicator when status !== "active".
- **Health Check**: `db.HealthCheck(ctx)` calls `PingContext` for readiness probes.
- **DB Connection Pool**: `MaxOpenConns(25)`, `MaxIdleConns(5)`, `ConnMaxLifetime(5min)`.
- **Context Propagation**: All DB queries and HTTP calls accept `context.Context` as first parameter for cancellation support.

## Testability Interfaces

The codebase uses consumer-side interfaces for testability:
- **`sentiment.SessionStateStore`** — abstracts session state (14 methods, in-memory or Redis). Two implementations: `InMemoryStore` (with `clockwork.Clock` injection) and `RedisStore` (with `clockwork.Clock` injection).
- **`sentiment.VoteStore`** — narrow 4-method interface (ISP-clean) for the vote processing pipeline: `GetSessionByBroadcaster`, `GetSessionConfig`, `CheckDebounce`, `ApplyVote`. Both `SessionStateStore` implementations satisfy it.
- **`sentiment.DebouncePruner`** — optional interface with `PruneDebounce(ctx)`. Only `InMemoryStore` implements it (Redis uses TTL-based expiry). Engine uses type assertion to call it.
- **`sentiment.ConfigStore`** — extracted from `*database.DB`, only exposes `GetConfig(ctx, userID)`. Allows mocking the DB in engine tests. Called in the caller's goroutine (not the actor), so mocks need a mutex.
- **`sentiment.Broadcaster`** — implemented by `websocket.Hub`. Allows mocking broadcasts in engine tests.
- **`sentiment.TwitchManager`** — `Unsubscribe(ctx, userID) error`. Used for cleanup tests. Called from a background goroutine, so mocks need a mutex.
- **`server.dataStore`** — subset of `*database.DB` used by handlers (`GetUserByID`, `GetUserByOverlayUUID`, `GetConfig`, `UpdateConfig`, `UpsertUser`, `RotateOverlayUUID`).
- **`server.sentimentService`** — subset of `*sentiment.Engine` used by handlers (`ActivateSession`, `ResetSession`, `MarkDisconnected`, `CleanupOrphans`, `UpdateSessionConfig`).
- **`server.twitchOAuthClient`** — `ExchangeCodeForToken(ctx, code) (*twitchTokenResult, error)`. Production implementation wraps Twitch HTTP APIs; tests use `mockOAuthClient`.
- **`server.twitchService`** — subset of `*twitch.SubscriptionManager` used by handlers (`Subscribe(ctx, userID, broadcasterUserID)`, `Unsubscribe(ctx, userID)`).
- **`server.webhookHandler`** — `HandleEventSub(c echo.Context) error`. Nil when webhooks not configured.
- **`twitch.conduitAPIClient`** — subset of `TwitchClient` used by `ConduitManager` (`CreateConduit`, `GetConduits`, `UpdateConduitShards`, `DeleteConduit`).
- **`twitch.subscriptionAPIClient`** — subset of `TwitchClient` used by `SubscriptionManager` (`CreateEventSubSubscription`, `DeleteEventSubSubscription`).
- **`clockwork.Clock`** — injected into `sentiment.Engine`, `sentiment.InMemoryStore`, and `sentiment.RedisStore` for deterministic time control in tests.

### Testing with the Actor Pattern

Since fire-and-forget commands (e.g., `ProcessVote`, `MarkDisconnected`) are asynchronous, tests use **barrier calls** to ensure ordering:
- Call a synchronous query like `GetSessionValue()` or `GetSessionConfig()` after fire-and-forget commands — the reply won't come until all prior commands in the channel are processed.
- When testing clock-dependent behavior, place a barrier **before** `clock.Advance()` to ensure timestamps are recorded at the expected time.
- `CleanupOrphans` fires unsubscribe calls in a background goroutine — tests use `time.Sleep(50ms)` after a barrier to wait for the goroutine.

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
- `internal/sentiment/engine_test.go` — vote logic, debounce, clamping, cleanup, reconnect, ticker, config updates (25 tests)
- `internal/sentiment/trigger_test.go` — MatchTrigger: for/against, case-insensitive, priority, nil config, empty message (7 tests)
- `internal/websocket/hub_test.go` — register, broadcast, lifecycle callbacks, client cap
- `internal/server/handlers_test.go` — shared test infrastructure: mocks (`mockDataStore`, `mockSentimentService`, `mockOAuthClient`), helpers (`newTestServer`, `setSessionUserID`, `withOAuthClient`)
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
- `errcheck` on `Close()`, `SetWriteDeadline()`, `SetReadDeadline()` for standard cleanup patterns
- `exitAfterDefer` — standard Go pattern in `main()` and `TestMain()`
- `middleware.Logger` deprecation — replacing requires larger refactor
- `bodyclose` in test files — false positive on WebSocket `Dial()`

## Go Module

Module: `github.com/pscheid92/chatpulse`, Go 1.25.7.

Key deps: echo/v4, gorilla/websocket, gorilla/sessions, Its-donkey/kappopher, lib/pq, redis/go-redis/v9, google/uuid, joho/godotenv, jonboulle/clockwork.

Test deps: stretchr/testify, jonboulle/clockwork, testcontainers-go, testcontainers-go/modules/postgres, testcontainers-go/modules/redis.

### Dependency Changes (Feb 2026 Architecture Overhaul)

| Removed | Added |
|---------|-------|
| `github.com/nicklaw5/helix/v2` | `github.com/Its-donkey/kappopher` v1.1.1 |
| | `github.com/redis/go-redis/v9` v9.17.3 |
| | `github.com/testcontainers/testcontainers-go/modules/redis` v0.40.0 |

### Files Deleted / Renamed

- `internal/twitch/eventsub.go` — 660-line EventSub WebSocket actor with 5-state FSM (replaced by webhooks + conduits)
- `internal/twitch/token.go` — Token refresh middleware (replaced by Kappopher's built-in token management)
- `internal/twitch/token_test.go` — Tests for deleted token code
- `internal/twitch/helix.go` → `internal/twitch/client.go` — Renamed `HelixClient` to `TwitchClient`, deleted unused `GetConduitShards`, `GetUsers`, `AppAuthClient` methods
- `internal/twitch/helix_test.go` → `internal/twitch/client_test.go` — Renamed alongside client
