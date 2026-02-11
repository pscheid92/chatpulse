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
cmd/server/main.go          Entry point (setupConfig, setupDB, setupRedis, initWebhooks, runGracefulShutdown)
internal/
  domain/
    errors.go               Sentinel errors (ErrUserNotFound, ErrConfigNotFound, ErrSubscriptionNotFound)
    user.go                 User type, UserRepository interface
    config.go               Config, ConfigSnapshot types, ConfigRepository interface
    session.go              SessionRepository interface (11 methods), SessionUpdate type
    sentiment.go            SentimentStore interface (ApplyVote, GetSentiment, ResetSentiment)
    debounce.go             Debouncer interface (CheckDebounce)
    engine.go               Engine interface (GetCurrentValue, ProcessVote, ResetSentiment)
    twitch.go               EventSubSubscription type, EventSubRepository, TwitchService interfaces
    app.go                  AppService interface
  config/config.go          Struct-tag-based env loading (go-simpler/env), .env file support (godotenv), validation
  crypto/crypto.go          crypto.Service interface, AesGcmCryptoService (AES-256-GCM), NoopService (plaintext passthrough)
  database/
    postgres.go             PostgreSQL connection (pgxpool.Pool), tern-based migrations (embedded SQL)
    user_repository.go      UserRepo: user CRUD, overlay UUID rotation (delegates encryption to crypto.Service)
    config_repository.go    ConfigRepo: config read/update
    eventsub_repository.go  EventSubRepo: EventSub subscription CRUD
    sqlc/schemas/           Schema DDL (shared: sqlc type analysis + tern runtime migrations)
    sqlc/queries/           SQL query definitions for sqlc code generation
    sqlcgen/                Generated Go code from sqlc
  app/service.go            Application layer: orchestrates session lifecycle, cleanup timer, config saves
  redis/
    client.go               Redis connection + Lua library loading (NewClient returns *redis.Client)
    chatpulse.lua           Lua library for Redis Functions (apply_vote, get_decayed_value)
    session_repository.go   SessionRepo: implements domain.SessionRepository (session lifecycle, queries, ref counting, orphan scanning)
    sentiment_store.go      SentimentStore: implements domain.SentimentStore (vote application, decay reads, sentiment reset via Redis Functions)
    debouncer.go            Debouncer: implements domain.Debouncer (SETNX-based per-user debounce with 1s TTL)
  sentiment/
    engine.go               Engine: GetCurrentValue (time-decayed read) + ProcessVote (vote pipeline) + matchTrigger (unexported)
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
    eventsub.go             EventSubManager: Twitch API client, conduit lifecycle, DB-backed subscription management
    webhook.go              EventSub webhook handler (HMAC verification, direct-to-store voting)
  broadcast/
    broadcaster.go          Broadcaster: actor pattern, pull-based tick loop, per-session client management
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

PostgreSQL 15+. Schema managed by [tern](https://github.com/jackc/tern) migrations embedded from `internal/database/sqlc/schemas/`. Three tables:
- `users` — Twitch OAuth data, tokens (encrypted at rest if `TOKEN_ENCRYPTION_KEY` set), expiry, `overlay_uuid` (separate from internal `id`)
- `configs` — Per-user sentiment config (triggers, labels, decay speed)
- `eventsub_subscriptions` — Tracks active EventSub subscriptions per user (user_id FK, broadcaster_user_id, subscription_id, conduit_id)

Database connection returns a bare `*pgxpool.Pool` — no wrapper struct. Repositories accept the pool directly via constructors.

## Architecture Notes

### Scaling Model

Redis-only architecture. Session state lives in Redis, Redis Functions handle atomic vote/decay operations (loaded via `FUNCTION LOAD`, called via `FCALL`/`FCALL_RO`). Multiple instances share state via Redis; ref counting (`IncrRefCount`/`DecrRefCount`) tracks how many instances serve each session. The `Broadcaster` pulls current values from Redis on each tick (50ms) rather than subscribing to push notifications.

### Bot Account Architecture

A single dedicated bot account reads chat on behalf of all streamers:

- **Bot account** authorizes `user:read:chat` + `user:bot` scopes for the app (one-time setup)
- **Streamers** only grant `channel:bot` scope during OAuth login, allowing the bot into their channel
- EventSub subscriptions set `user_id` to the bot's Twitch user ID and `broadcaster_user_id` to the streamer
- `BOT_USER_ID` env var provides the bot's Twitch user ID; stored in `EventSubManager` and threaded to `CreateEventSubSubscription`

### EventSub Transport: Webhooks + Conduits

Chat messages are received via **Twitch EventSub webhooks** transported through a **Conduit**:

- **EventSub Manager** (`twitch/eventsub.go`): Owns the Kappopher `*helix.Client` directly (no separate wrapper). `NewEventSubManager(clientID, clientSecret, ...)` validates credentials via `GetAppAccessToken` (fail-fast) and creates the Helix client internally. Manages conduit lifecycle and EventSub subscriptions. On startup (`Setup`), finds an existing conduit via `GetConduits()` or creates a new one, then configures a webhook shard pointing to the app's `WEBHOOK_CALLBACK_URL`. Creates/deletes EventSub subscriptions targeting the conduit. Handles Twitch 409 Conflict via `helix.APIError` type assertion (subscription already exists) as idempotent success. Subscriptions are persisted in PostgreSQL (`eventsub_subscriptions` table) for idempotency and crash recovery. On subscribe failure after Twitch API success, attempts cleanup. On shutdown (`Cleanup`), deletes the conduit.
- **Webhook Handler** (`twitch/webhook.go`): Receives Twitch POST notifications with Kappopher's built-in HMAC-SHA256 signature verification. Processes votes via `engine.ProcessVote()` through the `domain.Engine` interface. Flow: broadcaster lookup → trigger match → debounce check → atomic vote application.

### Concurrency Model: Actor Pattern

The `Broadcaster` (`broadcast/broadcaster.go`) uses the **actor pattern** — a single goroutine owns all mutable state and receives typed commands via a buffered channel. No mutexes on actor-owned state.

The actor follows this shape:
- Buffered command channel for typed commands (fire-and-forget or request/reply via embedded reply channel)
- Single `run()` goroutine with `select` on commands and ticker, `default` case logging unknown command types
- `Stop()` method for clean shutdown
- Command types use an embedded `baseBroadcasterCmd` struct to satisfy the marker interface

### Broadcaster (`broadcast/broadcaster.go`)

- Actor goroutine owns the `activeClients` map (session UUID → set of `*clientWriter`). `NewBroadcaster` accepts a `domain.Engine`, an `onFirstClient` callback, an `onSessionEmpty` callback, and a `clockwork.Clock`.
- **Pull-based broadcasting**: A 50ms ticker calls `engine.GetCurrentValue()` for each active session and broadcasts `domain.SessionUpdate{Value, Status: "active"}` to all connected clients. JSON marshaling happens once per session before fanning out.
- **Per-connection write goroutines** (`clientWriter` in `writer.go`): each WebSocket connection gets its own goroutine with a buffered send channel (cap 16), 5-second write deadlines, and periodic ping/pong heartbeat (30s ping interval, 60s pong deadline). Slow clients are disconnected (non-blocking send) instead of blocking all broadcasts.
- **Per-session client cap**: `maxClientsPerSession = 50` prevents resource exhaustion.
- **Session empty callback**: When the last client disconnects from a session, the `onSessionEmpty` callback fires (wired to `app.Service.OnSessionEmpty` in main.go), which decrements the Redis ref count and marks the session as disconnected if no instances serve it.

### Sentiment Engine (`sentiment/engine.go`)

- No actor, no goroutines. All mutable state lives in Redis.
- Depends on three focused interfaces: `domain.SessionRepository` (session queries), `domain.SentimentStore` (vote/decay operations), and `domain.Debouncer` (per-user rate limiting).
- `GetCurrentValue(ctx, sessionUUID)` reads the session config, then calls `sentimentStore.GetSentiment()` with the current clock time. The Redis Function computes the time-decayed value atomically.
- `ProcessVote(ctx, broadcasterUserID, chatterUserID, messageText)` encapsulates the full vote pipeline: broadcaster lookup → trigger match → debounce check → atomic vote application.
- `ResetSentiment(ctx, sessionUUID)` delegates to `sentimentStore.ResetSentiment()`.
- Implements `domain.Engine`, consumed by both the `Broadcaster`'s tick loop (reads), the webhook handler (writes), and the app layer (reset).

### SessionRepository Interface (`domain/session.go`)

Abstracts session lifecycle, queries, ref counting, and orphan cleanup (11 methods including `IncrRefCount`/`DecrRefCount` for multi-instance ref counting). Single implementation:

- **`SessionRepo`** (`redis/session_repository.go`): Directly implements `domain.SessionRepository`. Takes `*redis.Client` + `clockwork.Clock`. Mirrors the `database.UserRepo` pattern — single type, single constructor, implements domain interface directly.

### SentimentStore Interface (`domain/sentiment.go`)

Abstracts atomic vote/decay operations (3 methods: `ApplyVote`, `GetSentiment`, `ResetSentiment`). Single implementation:

- **`SentimentStore`** (`redis/sentiment_store.go`): Implements `domain.SentimentStore`. Takes `*redis.Client`; calls Redis Functions (`FCALL`/`FCALL_RO`) for atomic vote application and time-decayed reads. Lua library loaded by `NewClient` at connection time.

### Debouncer Interface (`domain/debounce.go`)

Abstracts per-user vote rate limiting (1 method: `CheckDebounce`). Single implementation:

- **`Debouncer`** (`redis/debouncer.go`): Implements `domain.Debouncer`. Uses Redis `SETNX` with 1s TTL (auto-expires, no pruning needed).

### Vote Processing Pipeline

Votes flow through `engine.ProcessVote()`, which orchestrates three focused interfaces (`SessionRepository` for queries, `SentimentStore` for vote application, `Debouncer` for rate limiting). The webhook handler calls this method through the `domain.Engine` interface. The bot account's `user_id` in the EventSub subscription means Twitch sends all chat messages from channels the bot has joined:

1. Twitch sends `channel.chat.message` webhook → Kappopher verifies HMAC
2. `sessions.GetSessionByBroadcaster(broadcasterUserID)` → session UUID
3. `sessions.GetSessionConfig(sessionUUID)` → config
4. `sentiment.matchTrigger(messageText, config)` → delta (+10, -10, or 0)
5. `debouncer.CheckDebounce(sessionUUID, chatterUserID)` → allowed (SETNX in Redis)
6. `sentimentStore.ApplyVote(sessionUUID, delta)` → new value (Redis Function: decay + clamp)

Step 6's Redis Function atomically applies the time-decayed vote in Redis. The `Broadcaster`'s next tick (≤50ms later) reads the updated value via `Engine.GetCurrentValue` and broadcasts to local WebSocket clients.

### Redis Architecture (`internal/redis/`)

**Key schema**:
- `session:{overlayUUID}` — hash: `value`, `broadcaster_user_id`, `config_json`, `last_disconnect`, `last_update`
- `ref_count:{overlayUUID}` — integer ref count (how many instances serve this session)
- `broadcaster:{twitchUserID}` — string → overlayUUID
- `debounce:{overlayUUID}:{twitchUserID}` — key with 1s TTL (auto-expires)

**Redis Functions** (`chatpulse` library, loaded via `FUNCTION LOAD`, called via `FCALL`/`FCALL_RO`):
- `apply_vote`: `HGET value + last_update → apply time-decay → clamp(decayed + delta, -100, 100) → HSET value + last_update` (read-write, `FCALL`)
- `get_decayed_value`: `HGET value + last_update → compute time-decayed value → return` (read-only with `no-writes` flag, `FCALL_RO`)

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
- Accepts `domain.UserRepository`, `domain.ConfigRepository`, `domain.SessionRepository`, `domain.Engine`, and `domain.TwitchService` (nil-safe for the Twitch dependency when webhooks are not configured). `ResetSentiment` delegates to the `Engine`, which in turn delegates to `SentimentStore`.
- Also exposes 4 pass-through read methods (`GetUserByID`, `GetUserByOverlayUUID`, `GetConfig`, `UpsertUser`) so handlers never access repositories directly.

### Domain Package (`internal/domain/`)

Concept-oriented files containing all shared types and cross-cutting interfaces:
- **`errors.go`**: Sentinel errors `ErrUserNotFound`, `ErrConfigNotFound`, `ErrSubscriptionNotFound` — returned by repository `Get*` methods when no row matches and by `Update*` methods when 0 rows are affected. Repositories translate `pgx.ErrNoRows` (for `:one` queries) and zero `RowsAffected()` (for `:execresult` queries) into these domain errors so consumers never import `pgx` directly.
- **`user.go`**: `User` type, `UserRepository` interface (5 methods)
- **`config.go`**: `Config`, `ConfigSnapshot` types, `ConfigRepository` interface (2 methods)
- **`session.go`**: `SessionRepository` interface (11 methods), `SessionUpdate` type
- **`sentiment.go`**: `SentimentStore` interface (3 methods: `ApplyVote`, `GetSentiment`, `ResetSentiment`)
- **`debounce.go`**: `Debouncer` interface (1 method: `CheckDebounce`)
- **`engine.go`**: `Engine` interface (3 methods: `GetCurrentValue`, `ProcessVote`, `ResetSentiment`)
- **`twitch.go`**: `EventSubSubscription` type, `EventSubRepository` (4 methods), `TwitchService` (2 methods)
- **`app.go`**: `AppService` interface (10 methods)
- Imports only stdlib + `github.com/google/uuid` — no internal dependencies, preventing circular imports.

### Other Architecture Notes

- **Trigger Matching**: `matchTrigger()` — unexported pure function in `engine.go`, case-insensitive exact match (trimmed). "For" trigger takes priority when both match. Returns +`voteDelta`, -`voteDelta`, or 0 (`voteDelta` = 10.0).
- **Vote Clamping**: Values clamped to [-100, 100].
- **Debounce**: 1 second per user per session. `domain.Debouncer` interface, implemented by `redis.Debouncer` using `SETNX` with 1s TTL (auto-expires, no pruning needed).
- **Overlay UUID Separation**: The overlay URL uses a separate `overlay_uuid` column (not the user's internal `id`). Users can rotate their overlay UUID via `POST /api/rotate-overlay-uuid` to invalidate old URLs.
- **Token Encryption at Rest**: AES-256-GCM encryption for access/refresh tokens in PostgreSQL. Handled by `crypto.Service` interface — `AesGcmCryptoService` (production, takes 64-char hex key) or `NoopService` (dev/test, plaintext passthrough). Injected into `UserRepo` via constructor. Nonce prepended to ciphertext, hex-encoded for TEXT column storage.
- **UpsertUser Transaction**: User creation and default config insertion are wrapped in a single database transaction for atomicity. Lives in `UserRepo.Upsert()`. Uses `toDomainUser()` helper for consistent row mapping + token decryption.
- **Connection Status Broadcasting**: Broadcaster sends `{"value": float, "status": "active"}` on each tick. Overlay shows "reconnecting..." indicator when status !== "active".
- **Context Propagation**: All DB queries and HTTP calls accept `context.Context` as first parameter for cancellation support.

## Testability Interfaces

The codebase uses consumer-side interfaces for testability. Cross-cutting interfaces live in `domain/` (split across concept-oriented files); package-private interfaces stay with their single consumer.

**Domain interfaces** (in `domain/` package):
- **`domain.SessionRepository`** (`session.go`) — abstracts session lifecycle, queries, ref counting, orphan cleanup (11 methods). Implemented by `redis.SessionRepo` (with `clockwork.Clock` injection).
- **`domain.SentimentStore`** (`sentiment.go`) — abstracts atomic vote/decay operations (3 methods: `ApplyVote`, `GetSentiment`, `ResetSentiment`). Implemented by `redis.SentimentStore`.
- **`domain.Debouncer`** (`debounce.go`) — abstracts per-user vote rate limiting (1 method: `CheckDebounce`). Implemented by `redis.Debouncer`.
- **`domain.Engine`** (`engine.go`) — 3-method interface: `GetCurrentValue`, `ProcessVote`, `ResetSentiment`. Implemented by `sentiment.Engine`. Consumed by `broadcast.Broadcaster` (reads), `twitch.WebhookHandler` (writes), and `app.Service` (reset).
- **`domain.UserRepository`** — 5-method interface (`GetByID`, `GetByOverlayUUID`, `Upsert`, `UpdateTokens`, `RotateOverlayUUID`). Implemented by `database.UserRepo`.
- **`domain.ConfigRepository`** — 2-method interface (`GetByUserID`, `Update`). Implemented by `database.ConfigRepo`.
- **`domain.EventSubRepository`** — 4-method interface (`Create`, `GetByUserID`, `Delete`, `List`). Implemented by `database.EventSubRepo`.
- **`domain.AppService`** — 10-method contract between server and app layer. Includes 4 read pass-throughs (`GetUserByID`, `GetUserByOverlayUUID`, `GetConfig`, `UpsertUser`) + 6 operations (`EnsureSessionActive`, `IncrRefCount`, `OnSessionEmpty`, `ResetSentiment`, `SaveConfig`, `RotateOverlayUUID`). Implemented by `app.Service`. Handlers depend only on this interface — no direct repository access.
- **`domain.TwitchService`** — `Subscribe`, `Unsubscribe`. Implemented by `twitch.EventSubManager`.

**Package-private interfaces** (single consumer, stay local):
- **`server.twitchOAuthClient`** — `ExchangeCodeForToken(ctx, code) (*twitchTokenResult, error)`. Production implementation wraps Twitch HTTP APIs; tests use `mockOAuthClient`.
- **`server.webhookHandler`** — `HandleEventSub(c echo.Context) error`. Nil when webhooks not configured.
- **`twitch.subscriptionStore`** — 3-method interface (`Create`, `GetByUserID`, `Delete`) satisfied by `database.EventSubRepo`; used by `EventSubManager`.
- **`clockwork.Clock`** — injected into `sentiment.Engine`, `redis.SessionRepo`, `app.Service`, and `broadcast.Broadcaster` (threaded to `clientWriter` for write deadlines, ping tickers, and pong read deadlines) for deterministic time control in tests.

### Testing with the Actor Pattern

The `Broadcaster` uses an actor pattern. Tests use synchronous queries (e.g., `GetClientCount()`) as **barrier calls** — the reply won't come until all prior commands in the channel are processed. For `CleanupOrphans` (in `app.Service`), Twitch unsubscribe calls run in a background goroutine — tests use a channel with timeout to wait for completion.

## Testing

141 tests across 8 packages. Run with `make test` or `go test ./...`.

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
- `internal/crypto/crypto_test.go` — AesGcmCryptoService: key validation, encrypt/decrypt roundtrip, unique nonces, tampered ciphertext, NoopService passthrough (9 tests)
- `internal/database/postgres_test.go` — connection, tern migrations, schema verification (testcontainers)
- `internal/database/user_repository_test.go` — user CRUD, token encryption via crypto.Service, overlay UUID rotation, not-found error paths, encryption key mismatch (testcontainers)
- `internal/database/config_repository_test.go` — config read/update, not-found error path (testcontainers)
- `internal/database/eventsub_repository_test.go` — EventSub subscription CRUD, upsert, list; includes `createTestUser` helper (testcontainers)
- `internal/redis/integration_test.go` — test setup: testcontainers Redis, `setupTestClient` helper
- `internal/redis/client_integration_test.go` — Redis Function loading test (testcontainers)
- `internal/redis/session_repository_integration_test.go` — session CRUD, broadcaster mapping, ref counting, orphan listing (testcontainers)
- `internal/redis/sentiment_store_integration_test.go` — vote application, clamping, decay, sentiment read (testcontainers)
- `internal/redis/debouncer_integration_test.go` — debounce TTL behavior (testcontainers)
- `internal/sentiment/engine_test.go` — GetCurrentValue (4 tests) + ProcessVote (8 tests) + matchTrigger (9 tests): three focused mocks (`mockSessionRepo`, `mockSentimentStore`, `mockDebouncer`)
- `internal/app/service_test.go` — EnsureSessionActive, OnSessionEmpty, IncrRefCount, ResetSentiment, SaveConfig, RotateOverlayUUID, CleanupOrphans, Stop (14 tests)
- `internal/broadcast/broadcaster_test.go` — register, tick broadcast, multiple clients, session empty callback, client cap, value updates (7 tests)
- `internal/server/handlers_test.go` — shared test infrastructure: mocks (`mockAppService`, `mockOAuthClient`), helpers (`newTestServer`, `setSessionUserID`, `withOAuthClient`)
- `internal/server/handlers_unit_test.go` — input validation tests (validateConfig: 100% coverage)
- `internal/server/handlers_auth_test.go` — requireAuth middleware, login page, logout, OAuth callback (success, missing code, invalid state, exchange error, DB error) (10 tests)
- `internal/server/handlers_dashboard_test.go` — handleDashboard, handleSaveConfig (5 tests)
- `internal/server/handlers_api_test.go` — handleResetSentiment, handleRotateOverlayUUID (5 tests)
- `internal/server/handlers_overlay_test.go` — handleOverlay (3 tests)
- `internal/twitch/webhook_test.go` — webhook handler tests with HMAC-signed requests: trigger matching, debounce, invalid signature, non-chat events (5 tests)

### Test Dependencies

- **`stretchr/testify`** — assertions and require helpers
- **`jonboulle/clockwork`** — fake clock for deterministic time control
- **`testcontainers-go`** — container orchestration (v0.40.0)
- **`testcontainers-go/modules/postgres`** — PostgreSQL module
- **`testcontainers-go/modules/redis`** — Redis module

### Database Integration Tests (testcontainers)

**Setup**: `TestMain()` starts a PostgreSQL 15 container once, reused across all tests (~2-5s overhead total)
**Cleanup**: Each test uses `setupTestDB(t)` which registers a cleanup function to `TRUNCATE users, configs, eventsub_subscriptions CASCADE`
**Production fidelity**: Real PostgreSQL behavior, real schema, real constraints, real encryption

### Redis Integration Tests (testcontainers)

**Setup**: `TestMain()` starts a Redis 7 container once, reused across all tests (~1-2s overhead)
**Cleanup**: Each test calls `FlushAll` before running
**Tests cover**: Session CRUD, broadcaster mapping, ref counting, orphan scanning (SessionRepo); vote application, clamping, decay, sentiment reads (SentimentStore); debounce TTL (Debouncer); Redis Function loading (client)

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

Module: `github.com/pscheid92/chatpulse`, Go 1.26.0.

Key deps: echo/v4, gorilla/websocket, gorilla/sessions, Its-donkey/kappopher, jackc/pgx/v5, jackc/tern/v2, redis/go-redis/v9, google/uuid, joho/godotenv, jonboulle/clockwork, go-simpler/env.

Test deps: stretchr/testify, jonboulle/clockwork, testcontainers-go, testcontainers-go/modules/postgres, testcontainers-go/modules/redis.

### Dependency Changes (Feb 2026 Architecture Overhaul)

| Removed | Added |
|---------|-------|
| `github.com/nicklaw5/helix/v2` | `github.com/Its-donkey/kappopher` v1.1.1 |
| | `github.com/jackc/tern/v2` v2.3.5 |
| | `github.com/redis/go-redis/v9` v9.17.3 |
| | `github.com/testcontainers/testcontainers-go/modules/redis` v0.40.0 |
| | `go-simpler.org/env` v0.12.0 |

### Files Deleted / Renamed

- `internal/twitch/eventsub.go` — 660-line EventSub WebSocket actor with 5-state FSM (replaced by webhooks + conduits)
- `internal/twitch/token.go` — Token refresh middleware (replaced by Kappopher's built-in token management)
- `internal/twitch/token_test.go` — Tests for deleted token code
- `internal/twitch/helix.go` → `internal/twitch/client.go` → merged into `internal/twitch/eventsub.go` — Helix API wrapper merged into `EventSubManager`; `eventsubAPIClient` interface removed
- `internal/sentiment/store_redis.go` → `internal/redis/store.go` — Moved Redis store adapter out of domain package; renamed `RedisStore` to `SentimentStore` (later consolidated into `SessionStore`)
- `internal/models/user.go` — Model types (`User`, `Config`, `ConfigSnapshot`, `EventSubSubscription`) moved to `internal/domain/`; `models` package deleted
- `internal/sentiment/store.go` — `SessionStore`, `VoteStore`, `SessionUpdate` moved to `internal/domain/`
- `internal/sentiment/vote.go` — `ProcessVote` moved to `Engine.ProcessVote` method in `engine.go`; `domain.VoteStore` interface removed (Engine uses `SessionStore` directly)
- `internal/sentiment/trigger.go` — `MatchTrigger` moved into `engine.go` as unexported `matchTrigger`; tests merged into `engine_test.go`
- `internal/domain/domain.go` — Split into concept-oriented files: `errors.go`, `user.go`, `config.go`, `session.go`, `engine.go`, `twitch.go`, `app.go`. `SessionStateStore` renamed to `SessionStore`, `ScaleProvider` renamed to `Engine`, dead `GetSessionValue` method removed.
- `internal/sentiment/store_memory.go` — In-memory store removed (Redis-only architecture)
- `internal/websocket/hub.go` → `internal/broadcast/broadcaster.go` — Replaced push-based Hub with pull-based `Broadcaster` (package renamed from `websocket` to `broadcast`, type renamed from `OverlayBroadcaster` to `Broadcaster`)
- `internal/websocket/commands.go` — Hub command types removed (broadcaster defines its own internally)
- `internal/redis/pubsub.go` — Pub/Sub removed (broadcaster pulls values instead of subscribing)
- `internal/twitch/conduit.go` + `internal/twitch/subscription.go` → `internal/twitch/eventsub.go` — Merged `ConduitManager` and `SubscriptionManager` into single `EventSubManager`; `conduitID` internalized
- `internal/database/postgres.go` — Removed `DB` wrapper struct (was `*pgxpool.Pool` + `cipher.AEAD`); `Connect()` now returns bare `*pgxpool.Pool`; inline migrations replaced by tern; `HealthCheck()` removed
- Token encrypt/decrypt methods extracted from `UserRepo` → new `internal/crypto/crypto.go` (`crypto.Service` interface)
- `sqlc/` (repo root) → `internal/database/sqlc/` — Moved sqlc schemas + queries under database package for `//go:embed` compatibility; `schema.sql` renamed to `001_initial.sql` (tern migration format, dual-use with sqlc)
- `internal/redis/scripts.go` → `internal/redis/functions.go` → merged into `internal/redis/store.go` — Replaced `ScriptRunner` + `EVAL`/`EVALSHA` with `FunctionRunner` + `FCALL`/`FCALL_RO`; Lua code extracted to embedded `chatpulse.lua` library; then consolidated into `SessionStore`
- `internal/redis/scripts_test.go` → `internal/redis/functions_test.go` → merged into `internal/redis/store_test.go`
- `internal/redis/session.go` + `internal/redis/functions.go` + `internal/redis/store.go` — Consolidated three types (`SessionStore` + `FunctionRunner` + `SentimentStore`) into single `SessionRepo` in `store.go` that directly implements `domain.SessionRepository`; `session_test.go` + `functions_test.go` merged into `store_integration_test.go`. Later split into `session_repository.go` (SessionRepo), `sentiment_store.go` (SentimentStore), and `debouncer.go` (Debouncer).
- `domain.SessionStore` → `domain.SessionRepository` — Renamed to match `UserRepository`/`ConfigRepository` convention; implementation renamed `SentimentStore` → `SessionRepo`
- `domain.SessionRepository` (15 methods) → split into `SessionRepository` (11), `SentimentStore` (3), `Debouncer` (1) — Extracted `ApplyVote`/`GetSentiment`/`ResetSentiment` into `domain.SentimentStore` (`redis.SentimentStore`), `CheckDebounce` into `domain.Debouncer` (`redis.Debouncer`)
