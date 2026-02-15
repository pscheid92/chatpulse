# CLAUDE.md

## Project Overview

ChatPulse — a Go application that tracks real-time chat sentiment for Twitch streamers via an OBS browser source overlay. Uses a dedicated bot account to read chat on behalf of all streamers. Multi-tenant SaaS supporting horizontal scaling via Redis.

## Build & Run

```bash
make build          # Build binary → ./server
make run            # Build and run locally
make test           # All tests (unit + integration via testcontainers, ~15s)
make test-short     # Unit tests only (<2s, no Docker)
make test-race      # Tests with race detector
make test-coverage  # Generate coverage.out and coverage.html
make fmt            # go fmt ./...
make lint           # golangci-lint (configured via .golangci.yml)
make deps           # go mod download && go mod tidy
make docker-up      # Docker Compose (app + PostgreSQL 18 + Redis)
make docker-down    # Stop Docker Compose
```

Environment loaded from `.env` via `godotenv`, mapped to `Config` struct via `go-simpler/env` struct tags. See `.env.example`.

## Project Structure

```
cmd/server/main.go          Entry point (setupConfig, setupRedis, initWebhooks, runGracefulShutdown)
internal/
  domain/                   Interfaces + types only (no internal dependencies)
    errors.go               Sentinel errors (ErrUserNotFound, ErrConfigNotFound, ErrSubscriptionNotFound)
    user.go                 User type, UserRepository interface
    config.go               Config, ConfigSnapshot types, ConfigRepository interface
    session.go              SessionRepository interface (3 query methods), SessionUpdate type
    sentiment.go            SentimentStore interface (ApplyVote, GetRawSentiment, ResetSentiment)
    debounce.go             Debouncer interface
    engine.go               Engine interface, VoteResult enum, BroadcastData type
    twitch.go               EventSubSubscription type, EventSubRepository, TwitchService interfaces
    app.go                  UserService, ConfigService interfaces, SaveConfigRequest struct
  config/config.go          Struct-tag-based env loading, validation
  crypto/crypto.go          crypto.Service interface, AesGcmCryptoService (AES-256-GCM), NoopService
  database/
    postgres.go             PostgreSQL connection (pgxpool), tern migrations, pool stats exporter
    user_repository.go      UserRepo: user CRUD, overlay UUID rotation
    config_repository.go    ConfigRepo: config read/update
    eventsub_repository.go  EventSubRepo: EventSub subscription CRUD
    sqlc/schemas/           Schema DDL (tern migrations + sqlc type analysis)
    sqlc/queries/           SQL query definitions for sqlc
    sqlcgen/                Generated Go code from sqlc
  app/service.go            Application orchestration (session lifecycle, config saves, cleanup)
  redis/
    client.go               NewClient: connection, circuit breaker + metrics hooks, Lua library loading
    chatpulse.lua           Lua library: apply_vote + PUBLISH
    session_repository.go   SessionRepo: session queries (broadcaster lookup, config)
    sentiment_store.go      SentimentStore: vote application, raw value reads via Redis Functions
    debouncer.go            Debouncer: SETNX-based per-user debounce (1s TTL)
    config_cache.go         ConfigCacheRepo: read-through config cache (Redis → PostgreSQL)
    circuit_breaker_hook.go Circuit breaker for Redis operations
    metrics_hook.go         Prometheus metrics for Redis operations
  sentiment/
    engine.go               Engine: vote pipeline orchestration (trigger match → debounce → apply)
    config_cache.go         In-memory config cache (10s TTL, eviction timer)
  server/
    server.go               Echo server setup, lifecycle
    routes.go               Route definitions
    handlers*.go            HTTP handlers (auth, dashboard, API, overlay, health, metrics)
    connection_limiter.go   WebSocket connection limits (global, per-IP, rate limiting)
    oauth_client.go         twitchOAuthClient interface + HTTP implementation
  twitch/
    eventsub.go             EventSubManager: Twitch API client (Kappopher), conduit lifecycle, subscriptions with retry
    webhook.go              EventSub webhook handler (HMAC verification, timestamp freshness, vote processing)
  broadcast/
    broadcaster.go          Broadcaster: actor pattern, Redis pub/sub subscriber, degraded state detection
    writer.go               Per-connection write goroutine (buffered send, ping/pong, idle timeout)
  metrics/metrics.go        Prometheus metrics (Redis, Broadcaster, WebSocket, Vote, Database, Application)
  coordination/
    registry.go             Instance heartbeat and discovery
    pubsub.go               Pub/sub-based config cache invalidation
    leader.go               Leader election
  logging/logger.go         Structured logging setup (slog)
  version/version.go        Build version info (ldflags)
web/templates/
  login.html                Login page with Twitch OAuth button
  dashboard.html            Streamer config UI
  overlay.html              OBS overlay (glassmorphism, status indicator)
```

## Key Routes

```
GET  /health/live              Liveness probe
GET  /health/ready             Readiness probe (Redis, PostgreSQL, Redis Functions)
GET  /metrics                  Prometheus metrics
GET  /auth/login               Login page with Twitch OAuth button
GET  /auth/callback            Twitch OAuth callback
POST /auth/logout              Clear session, redirect to login
GET  /dashboard                Streamer config page (auth required)
POST /dashboard/config         Save config (auth required)
POST /api/reset/:uuid          Reset sentiment bar (auth required)
POST /api/rotate-overlay-uuid  Generate new overlay URL (auth required)
GET  /overlay/:uuid            Serve overlay page (public)
GET  /ws/overlay/:uuid         WebSocket endpoint (public)
POST /webhooks/eventsub        Twitch EventSub webhook receiver
```

## Environment Variables

See `.env.example` for all variables.

**Required**: `DATABASE_URL`, `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET`, `TWITCH_REDIRECT_URI`, `SESSION_SECRET`, `REDIS_URL`

**Webhook group** (all three required together):
- `WEBHOOK_CALLBACK_URL` — Public HTTPS URL for EventSub delivery
- `WEBHOOK_SECRET` — HMAC secret for signature verification (10-100 chars)
- `BOT_USER_ID` — Twitch user ID of the dedicated bot account

**Optional**: `TOKEN_ENCRYPTION_KEY`, `LOG_LEVEL`, `LOG_FORMAT`, `APP_ENV`, connection limits (`MAX_WEBSOCKET_CONNECTIONS`, `MAX_CONNECTIONS_PER_IP`, `CONNECTION_RATE_PER_IP`, `CONNECTION_RATE_BURST`), database pool settings (`DB_MIN_CONNS`, `DB_MAX_CONNS`, etc.)

## Architecture

### Scaling Model

Redis-only session state. Redis Functions handle atomic vote operations and publish updates via pub/sub. Multiple instances share state via Redis. Broadcaster subscribes to `sentiment:changes` pub/sub channel and fans out to WebSocket clients — zero Redis cost when idle.

### Data Consistency

**PostgreSQL = source of truth** (user config with `version` field). **Redis = session cache**. Eventual consistency via read-through cache + pub/sub invalidation. Config saves publish invalidation to all instances; each evicts local in-memory cache + Redis cache. Failure mode: TTL expiry fixes staleness (in-memory: 10s, Redis: 1h).

### Bot Account

Single dedicated bot reads chat for all streamers. Bot authorizes `user:read:chat` + `user:bot`; streamers grant `channel:bot` during OAuth. EventSub subscriptions: `user_id` = bot, `broadcaster_user_id` = streamer.

### EventSub: Webhooks + Conduits

- **EventSubManager** (`twitch/eventsub.go`): Owns `*helix.Client` directly (Kappopher). Constructor validates credentials via `GetAppAccessToken`. Manages conduit lifecycle and subscriptions with retry (exponential backoff, 429 rate limit handling). Subscriptions persisted in PostgreSQL. 409 Conflict = idempotent success.
- **WebhookHandler** (`twitch/webhook.go`): HMAC-SHA256 verification (Kappopher), message deduplication, timestamp freshness validation (10-min window). Skips vote processing when no viewers. Processes votes via `engine.ProcessVote()` with 5s timeout.
- **Graceful degradation**: EventSub setup failures don't prevent app startup.

### Vote Processing Pipeline

1. Twitch `channel.chat.message` webhook → HMAC verification + timestamp check
2. `hasViewers` check → skip if no viewers watching
3. `getConfig()` → config (3-layer cache: in-memory → Redis → PostgreSQL)
4. `matchTrigger()` → delta (+10, -10, or 0)
5. `debouncer.CheckDebounce()` → 1s per user
6. `sentimentStore.ApplyVote()` → atomic Redis Function + PUBLISH
7. Broadcaster pub/sub subscriber → fan out to WebSocket clients

Per-user debounce prevents individual spam. Fail-open on debounce errors.

### Broadcaster: Actor Pattern + Pub/Sub

Single goroutine owns all mutable state via buffered command channel. Separate subscriber goroutine listens on Redis `sentiment:changes` pub/sub.

- Push model: Lua script publishes `{broadcaster_id, value, timestamp}` → subscriber injects into actor → fan out to WebSocket clients
- Degraded state: pub/sub disconnect → broadcasts `{"status":"degraded"}` to clients, overlay dims
- Per-session client cap (50), slow client eviction, pub/sub reconnection with exponential backoff
- Client-side decay via `requestAnimationFrame` (60fps, zero server cost)

### Redis Architecture

**Keys**: `session:{overlayUUID}` (hash), `broadcaster:{twitchUserID}` (string → UUID), `debounce:{UUID}:{userID}` (1s TTL)

**Redis Functions**: `chatpulse` library v4 (apply_vote + PUBLISH)

**Pub/Sub**: `sentiment:changes` (vote updates), `config:invalidate` (cache invalidation)

**Circuit breaker**: Wraps all Redis operations, opens after 5 requests @ 60% failure rate, graceful degradation.

## Dependency Rules

```
Domain ← Infrastructure ← Application ← Server ← Main
```

- **Domain** → stdlib + `uuid` only (no internal deps)
- **Infrastructure** (database, redis, twitch, crypto) → Domain
- **Application** (app, sentiment, broadcast) → Infrastructure + Domain
- **Server** → Application + Domain
- **Main** → everything (wiring only)
- **Forbidden**: Domain → anything internal; Infrastructure ↔ Infrastructure; Server → Infrastructure directly

## Testing

```bash
make test-short     # Unit tests only (<2s, no Docker)
make test           # All tests (unit + integration, ~15s)
make test-race      # With race detector
make test-coverage  # Coverage report
```

**Unit tests**: `<feature>_test.go` — mocks for all external dependencies.
**Integration tests**: `<feature>_integration_test.go` — testcontainers for PostgreSQL and Redis. `TestMain()` starts containers once, cleanup via `TRUNCATE`/`FlushAll`.

**Testcontainers with Rancher Desktop** requires:
```bash
export DOCKER_HOST=unix://$HOME/.rd/docker.sock
export TESTCONTAINERS_RYUK_DISABLED=true
```

## Observability

Prometheus metrics at `GET /metrics` across 6 categories: Redis, Broadcaster, WebSocket, Vote processing, Database, Application. Structured logging via `slog` (configurable level/format). Build info metric via ldflags.

## Linting

golangci-lint v2 via `.golangci.yml`. Run with `make lint`.

## Go Module

`github.com/pscheid92/chatpulse`, Go 1.26.0

**Requires PostgreSQL 18+** (uses `uuidv7()` for time-ordered UUIDs).

**Key deps**: echo/v4, gorilla/websocket, gorilla/sessions, kappopher (Twitch Helix), pgx/v5, tern/v2, go-redis/v9, failsafe-go (circuit breaker), prometheus/client_golang, clockwork, testcontainers-go

# Agent Instructions

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

