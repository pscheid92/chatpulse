# CLAUDE.md

## Project Overview

Twitch Sentiment Overlay ("tow") - a Go application that tracks real-time chat sentiment for Twitch streamers via an OBS browser source overlay.

## Build & Run

```bash
make build          # Build binary → ./server
make run            # Build and run locally
make test           # Run all tests (go test -v ./...)
make fmt            # Format code (go fmt ./...)
make lint           # Run golangci-lint (default rules, no .golangci.yml)
make deps           # go mod download && go mod tidy
make docker-up      # Start with Docker Compose (app + PostgreSQL)
make docker-down    # Stop Docker Compose
make clean          # Remove build artifacts
```

Environment is loaded automatically via `godotenv` from `.env` (see `.env.example` for required variables).

## Project Structure

```
cmd/server/main.go          Entry point (dotenv → config → DB → migrations → server)
internal/
  config/config.go          Environment variable loading + validation
  database/postgres.go      PostgreSQL connection, migrations, CRUD, token encryption, health check
  models/user.go            User (with OverlayUUID), Config, ConfigSnapshot models
  server/
    server.go               Echo server setup, lifecycle, cleanup sweep
    routes.go               Route definitions
    handlers.go             HTTP handlers (OAuth, dashboard, API, WebSocket)
  sentiment/engine.go       Core engine: sessions, decay, debouncing, voting (actor pattern)
  twitch/
    helix.go                Twitch Helix API wrapper (EventSub create/delete)
    eventsub.go             EventSub WebSocket with 5-state machine (actor pattern)
    token.go                Token refresh middleware
  websocket/hub.go          WebSocket hub for overlay clients (actor pattern, per-conn writers)
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
- `POST /dashboard/config` - Save config (auth required, live-updates in-memory config)
- `POST /api/reset/:uuid` - Reset sentiment bar (auth required)
- `POST /api/rotate-overlay-uuid` - Generate new overlay URL (auth required)
- `GET /overlay/:uuid` - Serve overlay page (public, uses overlay UUID)
- `GET /ws/overlay/:uuid` - WebSocket endpoint (public, uses overlay UUID)

## Environment Variables

All defined in `.env.example`. Required: `DATABASE_URL`, `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET`, `TWITCH_REDIRECT_URI`, `SESSION_SECRET`. Optional: `TOKEN_ENCRYPTION_KEY` (64 hex chars = 32 bytes for AES-256-GCM token encryption at rest; if empty, tokens are stored in plaintext).

## Database

PostgreSQL 15+. Two tables auto-created via migrations in `postgres.go`:
- `users` - Twitch OAuth data, tokens (encrypted at rest if `TOKEN_ENCRYPTION_KEY` set), expiry, `overlay_uuid` (separate from internal `id`)
- `configs` - Per-user sentiment config (triggers, labels, decay speed)

## Architecture Notes

### Concurrency Model: Actor Pattern

All three core components use the **actor pattern** — a single goroutine owns all mutable state and receives typed commands via a buffered channel. No mutexes on actor-owned state. This eliminates race conditions, deadlocks, and cascading stalls structurally. The only mutex is on `HelixClient` (thin API wrapper, not an actor) to serialize `SetUserAccessToken` + API call pairs.

Each actor follows the same shape:
- Buffered `cmdCh` channel for commands (fire-and-forget or request/reply via embedded `replyCh`)
- Single `run()` goroutine with `for cmd := range cmdCh` loop
- `Stop()`/`Shutdown()` method for clean shutdown
- All blocking I/O (DB calls, HTTP calls, WebSocket dials) happens in background goroutines that send results back to the actor

### WebSocket Hub (`websocket/hub.go`)

- Actor goroutine owns the `clients` and `pendingClients` maps. `Register`, `Unregister`, `Broadcast`, `GetClientCount` all send commands.
- **Per-connection write goroutines** (`clientWriter`): each WebSocket connection gets its own goroutine with a buffered send channel (cap 16) and 5-second write deadlines. Slow clients are disconnected (non-blocking send) instead of blocking all broadcasts.
- **Async `onFirstConnect`**: registration is two-phase — the callback runs in a background goroutine while clients are queued in `pendingClients`. When the callback completes, `cmdFirstConnectResult` promotes or rejects all queued clients. This prevents DB I/O from blocking the hub actor.
- **Per-session client cap**: `maxClientsPerSession = 50` prevents resource exhaustion from excessive WebSocket connections.
- `Register` blocks for a reply (caller needs to know if `onFirstConnect` failed). All others are fire-and-forget or reply-channel.

### Sentiment Engine (`sentiment/engine.go`)

- Actor goroutine owns `activeSessions`, `broadcasterToSession`, and `debounceMap`.
- **DB calls happen outside the actor**: `ActivateSession` checks if session exists (fast actor query), does the DB call in the caller's goroutine, then sends the config to the actor. The actor never does I/O.
- **Ticker goroutine** sends `cmdTick` to the actor every 50ms. Actor applies decay and broadcasts (fire-and-forget to Hub).
- **Debounce pruning**: every ~50 seconds, stale debounce entries (>5s old) are pruned to prevent unbounded growth during long streams.
- `CleanupOrphans` fires Twitch unsubscribe calls in a background goroutine after removing orphans from the maps.

### EventSub Manager (`twitch/eventsub.go`)

- Actor goroutine owns connection state, subscriptions, and the state machine.
- **Read pump** (`readPump`): dedicated goroutine reads WebSocket messages and sends them to the actor as commands. Never calls handlers directly.
- **Async connect**: `startConnect` spawns a goroutine that dials the WebSocket and sends `cmdConnectResult` back. Actor checks generation to discard stale results.
- **Pending subscribe queue**: if `Subscribe` is called before connected, requests are queued and processed when the welcome message arrives.
- **Reconnect via `time.AfterFunc`**: no blocking `time.Sleep`. Infinite retries with exponential backoff capped at 60 seconds (never gives up).
- **Keepalive timeout validated**: clamped to 10–600 second range from Twitch's reported value.
- **Generation counter**: still used but only checked inside the actor loop (no scattered lock-check-unlock patterns).

### HTTP Server (`internal/server/`)

- **Echo Framework**: Uses Echo v4 with Logger and Recover middleware. Sessions via gorilla/sessions with 7-day expiry, secure cookies in production.
- **Auth Middleware** (`requireAuth`): Checks session for user ID, parses UUID, stores in Echo context. Redirects to `/auth/login` if not authenticated.
- **OAuth Flow**: `GET /auth/login` serves login page → user clicks Twitch button → Twitch redirects to `/auth/callback` with code → exchange code for tokens via Twitch OAuth API → fetch user info via Helix API → upsert user to DB → create session → redirect to `/dashboard`.
- **WebSocket Lifecycle**: `GET /ws/overlay/:uuid` → parse overlay UUID → lookup user by `GetUserByOverlayUUID` → upgrade to WebSocket → `ActivateSession` (idempotent, creates EventSub subscription on first connect) → `Subscribe` to EventSub → `Register` with Hub → read pump blocks until disconnect → `Unregister` from Hub → `MarkDisconnected` in Engine.
- **Lifecycle Callbacks**: Hub's `onFirstConnect` callback (wired in main.go) calls `ActivateSession` + `Subscribe`. Hub's `onLastDisconnect` callback calls `MarkDisconnected`. This decouples the Hub from domain logic.
- **Cleanup Timer**: Server starts a 30s ticker that calls `sentiment.CleanupOrphans(twitch)` to remove sessions with no clients for >30s.
- **Graceful Shutdown**: `SIGINT`/`SIGTERM` → `server.Shutdown` → stop cleanup timer → `echo.Shutdown` → `engine.Stop` → `eventSubManager.Shutdown` → `hub.Stop`.

### Other Architecture Notes

- **Vote Processing**: Trigger match (case-insensitive substring) → debounce (1s/user/session) → apply vote (±10, clamped [-100, 100]).
- **EventSub State Machine**: Disconnected → Connecting → Connected → ReconnectingGraceful (Twitch sends new URL, subs carry over) or ReconnectingUngraceful (connection dies, re-create all subs). Keepalive timeout = server-provided value (clamped) + 5s buffer.
- **Token Refresh**: `EnsureValidToken` checks expiry with 60s buffer, refreshes automatically (10s HTTP timeout), flags revoked tokens via `TokenRefreshError.Revoked`.
- **HelixClient Mutex**: `sync.Mutex` serializes `SetUserAccessToken` + API call pairs in `CreateEventSubSubscription` and `DeleteEventSubSubscription`. `EnsureValidToken` (DB + HTTP I/O) runs outside the lock to minimize contention.
- **Overlay UUID Separation**: The overlay URL uses a separate `overlay_uuid` column (not the user's internal `id`). Users can rotate their overlay UUID via `POST /api/rotate-overlay-uuid` to invalidate old URLs. `GetUserByOverlayUUID` looks up users for overlay/WebSocket handlers.
- **Cleanup Sweep**: 30s timer removes orphaned sessions and their broadcaster mappings. `ActivateSession` clears `LastClientDisconnect` on reconnect so that a reconnected client doesn't get cleaned up as an orphan.
- **EventSub Subscription**: `channel.chat.message` requires both `BroadcasterUserID` and `UserID` in the condition (both set to the broadcaster's Twitch user ID).
- **Context Propagation**: All DB queries and HTTP calls accept `context.Context` as first parameter for cancellation support. Background goroutines in EventSub use `context.Background()`. Engine's `ActivateSession` passes context to `db.GetConfig`.
- **Connection Status Broadcasting**: Engine broadcasts `{"value": float, "status": string}` via Hub. Status comes from `Engine.statusFn` (wired to `EventSubManager.GetConnectionStatus`). EventSub exposes status via `atomic.Value` (lock-free, single writer/reader). Overlay shows "reconnecting..." indicator when status !== "active".
- **Token Encryption at Rest**: AES-256-GCM encryption for access/refresh tokens in PostgreSQL. `Connect(databaseURL, encryptionKeyHex)` accepts a 32-byte hex key. If key is empty, tokens pass through unencrypted (dev/test mode). Nonce prepended to ciphertext, hex-encoded for TEXT column storage. Encryption in `UpsertUser`/`UpdateUserTokens`, decryption in `GetUserByID`/`GetUserByOverlayUUID`.
- **Health Check**: `db.HealthCheck(ctx)` calls `PingContext` for readiness probes.
- **DB Connection Pool**: `MaxOpenConns(25)`, `MaxIdleConns(5)`, `ConnMaxLifetime(5min)` — prevents stale connections after PostgreSQL restarts.

## Recent Bug Fixes & Improvements

### Critical Bug Fixes (Feb 2026)

**1. Eliminated Double Engine Creation Resource Leak**
- **Problem**: Engine was created twice in `main.go` (lines 51 and 77) to resolve circular dependency, causing goroutine and memory leaks
- **Solution**: Implemented `Engine.SetBroadcaster(hub)` method using actor pattern command (`cmdSetBroadcaster`) to set broadcaster after both Engine and Hub are created
- **Implementation**: Hub created with Engine reference in callbacks → Engine calls `SetBroadcaster(hub)` → `Start()` launches actors
- **Impact**: Eliminates leaked goroutines (ticker + actor), reduces memory footprint, maintains thread safety

**2. Added EventSub WebSocket Dial Timeout**
- **Problem**: `websocket.DefaultDialer.Dial()` had no timeout, could hang indefinitely if Twitch EventSub is unresponsive
- **Solution**: Defense in depth - both context timeout and dialer handshake timeout (15 seconds each)
- **Implementation**: `context.WithTimeout(15s)` + `websocket.Dialer{HandshakeTimeout: 15s}` in `startConnect()`
- **Impact**: Prevents indefinite hangs, reconnect logic retries with exponential backoff after timeout

**3. Fixed Session Invalidation on Logout**
- **Problem**: `session.Save()` errors ignored in logout handler, users could think they're logged out but session remains valid
- **Solution**: Check error and return 500 with actionable message if save fails
- **Impact**: Security fix - ensures sessions are properly invalidated, prevents unauthorized access

### High-Severity Bug Fixes

**4. Error Propagation from Hub.Register**
- **Problem**: `Hub.Register()` blocked on `errCh` but didn't return error to caller
- **Solution**: Changed signature to `func Register(...) error` and return `<-errCh`
- **Impact**: Handler can now detect and log registration failures (e.g., onFirstConnect errors)

**5. Removed Duplicate Activation Logic**
- **Problem**: `ActivateSession` and `Subscribe` called twice per connection - once in handler before `Register`, once in `onFirstConnect` callback
- **Solution**: Removed duplicate calls from handler, rely entirely on `onFirstConnect` callback
- **Impact**: Eliminates 50% of DB queries and Twitch API calls for new sessions, cleaner separation of concerns

**6. OAuth Flow Timeout Protection**
- **Problem**: OAuth callback handler could hang indefinitely if Twitch OAuth API is slow
- **Solution**: Added 10-second timeout context for entire OAuth flow (token exchange + user info fetch)
- **Implementation**: `context.WithTimeout(c.Request().Context(), 10s)` wrapping `exchangeCodeForToken()`
- **Impact**: Prevents handler goroutine leaks, better reliability under Twitch API issues

**7. Template Rendering Error Handling**
- **Problem**: Templates executed directly to `c.Response().Writer`, partial HTML sent on template errors
- **Solution**: Created `renderTemplate()` helper that renders to `bytes.Buffer` first, only sends on success
- **Implementation**: All handlers now use `renderTemplate(c, tmpl, data)` instead of `tmpl.Execute(c.Response().Writer, data)`
- **Impact**: No broken pages sent to users, proper 500 errors with no partial content

**8. Session Error Logging**
- **Problem**: Errors from `session.Get()` silently ignored in OAuth callback and logout handlers
- **Solution**: Added warning logs and fallback to `session.New()` on errors
- **Impact**: Better visibility for debugging session store issues

### Input Validation & Code Quality

**9. Comprehensive Config Input Validation**
- **Problem**: Dashboard config form inputs not validated, could save empty triggers, excessively long strings, invalid decay values
- **Solution**: Implemented `validateConfig()` with:
  - Empty checks (trimmed whitespace)
  - Length limits (500 chars for triggers, 50 for labels)
  - Decay range validation (0.1-2.0, matching template slider)
  - Identical trigger prevention (case-insensitive, since vote matching is case-insensitive)
- **Impact**: Prevents invalid data storage, better UX with clear error messages

**10. Template Variable Fix**
- **Problem**: Dashboard reset button used `{{.UserID}}` but handler expects `OverlayUUID`
- **Solution**: Changed template to `{{.OverlayUUID}}`
- **Impact**: Reset button now works correctly

### Code Quality Improvements

- **Enhanced error messages**: User-facing errors now include actionable guidance (e.g., "clear your browser cookies")
- **Improved error logging**: Template errors include request path for easier debugging
- **Fallback error handling**: Logout handler creates new session if `session.Get()` fails, with error logging
- **Documentation**: Added clear warning to `Engine.Start()` about `SetBroadcaster()` initialization order requirement
- **Validation**: Prevents users from setting identical "for" and "against" triggers

### Testing & Verification

All bug fixes verified with:
- ✅ **58/58 unit tests passing** (no regressions)
- ✅ **Build successful** with all changes
- ✅ **Manual testing** of error paths (timeout scenarios, invalid inputs, template errors)
- ✅ **Code review** - approved at 10/10 quality rating

## Testability Interfaces

The codebase uses consumer-side interfaces for testability:
- **`sentiment.ConfigStore`** — extracted from `*database.DB`, only exposes `GetConfig(ctx, userID)`. Allows mocking the DB in engine tests. Called in the caller's goroutine (not the actor), so mocks need a mutex.
- **`sentiment.Broadcaster`** — implemented by `websocket.Hub`. Allows mocking broadcasts in engine tests. `Broadcast(sessionUUID, value, status)` includes connection status.
- **`sentiment.TwitchManager`** — implemented by `twitch.EventSubManager`. Allows mocking EventSub in cleanup tests. `Unsubscribe` is called from a background goroutine, so mocks need a mutex.
- **`server.dataStore`** — subset of `*database.DB` used by handlers (`GetUserByID`, `GetUserByOverlayUUID`, `GetConfig`, `UpdateConfig`, `UpsertUser`, `RotateOverlayUUID`).
- **`server.sentimentService`** — subset of `*sentiment.Engine` used by handlers and lifecycle callbacks (`ActivateSession`, `ResetSession`, `MarkDisconnected`, `CleanupOrphans`, `UpdateSessionConfig`).
- **`server.twitchService`** — subset of `*twitch.EventSubManager` used by handlers (`Subscribe`, `Unsubscribe`, `IsReconnecting`).
- **`clockwork.Clock`** — injected into `sentiment.Engine` for deterministic time control in tests (debounce, cleanup, decay ticker).

### Testing with the Actor Pattern

Since fire-and-forget commands (e.g., `ProcessVote`, `MarkDisconnected`) are asynchronous, tests use **barrier calls** to ensure ordering:
- Call a synchronous query like `GetSessionValue()` or `GetSessionConfig()` after fire-and-forget commands — the reply won't come until all prior commands in the channel are processed.
- When testing clock-dependent behavior, place a barrier **before** `clock.Advance()` to ensure timestamps are recorded at the expected time.
- `CleanupOrphans` fires unsubscribe calls in a background goroutine — tests use `time.Sleep(50ms)` after a barrier to wait for the goroutine.

## Testing

108 tests across 7 packages. **Overall coverage: 37.4%**. Run with `make test` or `go test ./...`.

### Test Types

**Unit tests** (fast, no external dependencies):
- Run with `make test-short` or `go test -short ./...`
- Complete in <2 seconds
- Use mocks for all external dependencies

**Integration tests** (use real infrastructure):
- Run with `make test` or `go test ./...`
- Complete in ~15 seconds
- Use testcontainers for PostgreSQL, real WebSockets, mock HTTP servers for external APIs
- Automatically skipped with `-short` flag via `if testing.Short() { t.Skip(...) }`

**Coverage analysis**:
- Run with `make test-coverage` to generate `coverage.out` and `coverage.html`
- Race detection: `make test-race` (runs fast tests with `-race`)

### Test Files

- `internal/config/config_test.go` — env loading, defaults, validation, encryption key validation (84.0% coverage)
- `internal/database/postgres_test.go` — **NEW** CRUD operations, token encryption, migrations, health check (71.3% coverage, 28 tests)
- `internal/database/testhelpers.go` — **NEW** test fixtures for creating users and configs
- `internal/sentiment/engine_test.go` — vote logic, debounce, clamping, cleanup, reconnect, ticker (85.6% coverage)
- `internal/websocket/hub_test.go` — register, broadcast, lifecycle callbacks, client cap (87.1% coverage)
- `internal/server/handlers_unit_test.go` — **NEW** input validation tests (validateConfig: 100% coverage, 12 tests)
- `internal/twitch/token_test.go` — **EXPANDED** token refresh logic, error handling, HTTP mocking (refreshToken: 91.7% coverage, 11 tests)
- `internal/twitch/helix_test.go` — **NEW** Helix client orchestration, error propagation (4 tests)

### Test Dependencies

- **`stretchr/testify`** — assertions and require helpers
- **`jonboulle/clockwork`** — fake clock for deterministic time control
- **`testcontainers-go`** — PostgreSQL container orchestration (v0.40.0)
- **`testcontainers-go/modules/postgres`** — PostgreSQL module for testcontainers

### Database Integration Tests (testcontainers)

**Setup**: `TestMain()` starts a PostgreSQL 15 container once, reused across all tests (~2-5s overhead total)
**Cleanup**: Each test uses `setupTestDB(t)` which registers a cleanup function to `TRUNCATE users, configs CASCADE`
**Performance**: Container startup amortized across test suite, individual tests run in milliseconds
**Production fidelity**: Real PostgreSQL behavior, real schema, real constraints, real encryption

Tests cover:
- Connection management (valid/invalid URLs, encryption key validation)
- Health checks (ping with context cancellation)
- Schema migrations (idempotency, verification)
- Token encryption/decryption (AES-256-GCM with/without key, error handling)
- User CRUD (insert, update, upsert, lookups by ID and overlay UUID)
- Token refresh flow
- Config CRUD (default creation, updates)
- Overlay UUID rotation

**Timezone handling**: All tests use `time.Now().UTC()` to avoid local timezone issues when comparing with PostgreSQL timestamps

### Twitch API Tests (HTTP mocking)

**Token Refresher** (`internal/twitch/token_test.go`):
- HTTP mocking with `httptest.Server` for Twitch OAuth endpoint
- Success flow, error responses (400/401/500), timeout, network errors
- Malformed JSON, empty responses, context cancellation
- `refreshToken()` function: **91.7% coverage**

**Helix Client** (`internal/twitch/helix_test.go`):
- Mock `tokenRefresher` interface for testing orchestration logic
- Token refresh error propagation for EventSub operations
- Verifies error handling without calling real Twitch API

### Server Validation Tests (unit)

**Input Validation** (`internal/server/handlers_unit_test.go`):
- Empty trigger detection (for/against)
- Length limits (triggers: 500 chars, labels: 50 chars)
- Decay range validation (0.1-2.0)
- Identical trigger prevention (case-insensitive)
- Valid configuration acceptance (5 test cases)
- `validateConfig()` function: **100% coverage**

### Not Tested (Deferred)

- `twitch/eventsub.go` — 659-line actor with 5-state FSM and WebSocket management (complex, low ROI for unit testing; validated in production)
- `server/handlers.go` — OAuth flow, dashboard, API endpoints, WebSocket lifecycle (patterns established, can be added incrementally)
- `cmd/server/main.go` — application wiring (integration tested in production)

### Test Coverage by Package (February 2026)

| Package | Coverage | Key Metrics |
|---------|----------|-------------|
| **config** | 84.0% | Env loading, validation |
| **database** | 71.3% | CRUD, encryption, migrations |
| **sentiment** | 85.6% | Vote logic, actor pattern |
| **websocket** | 87.1% | Hub, lifecycle, client cap |
| **server** | 6.5% | Input validation (100% of validateConfig) |
| **twitch** | 10.6% | Token refresh (91.7%), helix orchestration |
| **OVERALL** | **37.4%** | **108 tests total** |

## Go Module

Module: `github.com/pscheid92/twitch-tow`, Go 1.25.7.

Key deps: echo/v4, gorilla/websocket, gorilla/sessions, nicklaw5/helix/v2, lib/pq, google/uuid, joho/godotenv.

Test deps: stretchr/testify, jonboulle/clockwork, testcontainers-go, testcontainers-go/modules/postgres.
