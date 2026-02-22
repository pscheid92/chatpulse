# CLAUDE.md

## Project Overview

ChatPulse — a Go application that tracks real-time chat sentiment for Twitch streamers via an OBS browser source overlay. Uses a dedicated bot account to read chat on behalf of all streamers. Multi-tenant SaaS supporting horizontal scaling via Redis.

## Build & Run

```bash
make build          # Build binary → ./server (with ldflags: Version, Commit, BuildTime)
make run            # Build and run locally
make test           # All tests (unit + integration via testcontainers, ~15s)
make test-short     # Unit tests only (<2s, no Docker)
make test-race      # Tests with race detector (short mode)
make test-coverage  # Generate coverage.out and coverage.html
make fmt            # go fmt ./...
make lint           # golangci-lint (configured via .golangci.yml)
make deps           # go mod download && go mod tidy
make sqlc           # Regenerate sqlc code
make docker-up      # Docker Compose (app + PostgreSQL 18 + Redis 8)
make docker-down    # Stop Docker Compose
make docker-build   # Build Docker image
make clean          # Remove build artifacts
```

Environment loaded from `.env` via `godotenv`, mapped to `Config` struct via `go-simpler/env` struct tags. See `.env.example`.

## Project Structure

```
cmd/server/main.go              Entry point (setupConfig, setupDatabase, setupRedis, setupWebSocket, initEventSubManager, runGracefulShutdown)
internal/
  domain/                       Interfaces + types only (no internal dependencies)
    errors.go                   Sentinel errors (ErrStreamerNotFound, ErrConfigNotFound, ErrSubscriptionNotFound)
    streamer.go                 Streamer type, StreamerRepository interface
    config.go                   OverlayConfig, OverlayConfigWithVersion, ConfigRepository, ConfigSource interfaces
    overlay.go                  Overlay interface (ProcessMessage, Reset), VoteResult, DisplayMode, VoteTarget
    sentiment.go                SentimentStore interface (RecordVote, GetSnapshot, ResetSentiment), WindowSnapshot
    debounce.go                 ParticipantDebouncer interface (IsDebounced)
    pubsub.go                   EventPublisher interface (PublishSentimentUpdated, PublishConfigChanged)
    twitch.go                   EventSubSubscription type, EventSubService, EventSubRepository interfaces
  app/                          Business logic (application layer)
    service.go                  Service: orchestration (streamer CRUD, config save, sentiment reset, overlay UUID rotation)
    overlay.go                  Overlay: vote pipeline (trigger match → VoteTarget → debounce → RecordVote → WindowSnapshot)
    ticker.go                   SnapshotTicker: periodic sentiment refresh so old votes visually expire from sliding window
  adapter/
    postgres/                   PostgreSQL adapter
      postgres.go               Connection (pgxpool), tern migrations with advisory lock
      streamer_repository.go    StreamerRepo: CRUD, overlay UUID rotation, token encrypt/decrypt
      config_repository.go      ConfigRepo: read/update with optimistic locking (version field)
      eventsub_repository.go    EventSubRepo: EventSub subscription CRUD
      sqlc/schemas/             Tern migration DDL (streamers, configs, eventsub_subscriptions tables)
      sqlc/queries/             SQL query definitions for sqlc (13 named queries)
      sqlcgen/                  Generated Go code from sqlc
    redis/                      Redis adapter
      client.go                 NewClient: connection + ping
      sentiment_store.go        SentimentStore: Redis Streams (XADD/XTRIM/XRANGE pipeline)
      debouncer.go              ParticipantDebouncer: SETNX-based per-user debounce (1s TTL)
      config_cache.go           ConfigCacheRepo: 3-layer read-through config cache (in-memory → Redis → PostgreSQL)
      config_invalidation.go    Pub/sub-based config cache invalidation across instances
    twitch/                     Twitch adapter
      eventsub.go               EventSubManager: Twitch API client (Kappopher), conduit lifecycle, subscriptions with retry
      webhook.go                WebhookHandler: HMAC verification, timestamp freshness, viewer presence check, vote processing
    httpserver/                 HTTP adapter (Echo v4)
      server.go                 Echo server setup, session store, template parsing, lifecycle
      routes.go                 Route registration (rate limiters, security headers, metrics middleware)
      middleware.go             Correlation IDs, request logging, error handling, CSRF protection
      ratelimit.go              Per-IP rate limiting (Echo middleware with in-memory store)
      handlers_auth.go          Login, OAuth callback (exchange + upsert + EventSub subscribe), logout
      handlers_dashboard.go     Dashboard page, save config with validation
      handlers_api.go           Reset sentiment, rotate overlay UUID, overlay viewer status
      handlers_overlay.go       Serve overlay page, Centrifuge auth middleware
      handlers_health.go        Health probes (startup, liveness, readiness), version endpoint
      oauth_client.go           twitchOAuthClient interface + HTTP implementation
    websocket/                  WebSocket adapter (Centrifuge)
      node.go                   Centrifuge node setup, Redis broker/presence, overlay UUID → channel subscription
      origin.go                 WebSocket origin validation (prevents cross-site hijacking)
      publisher.go              Sentiment update publisher (forRatio, againstRatio, totalVotes, displayMode)
    metrics/                    Prometheus metrics adapter
      metrics.go                Registry setup (Go runtime + process collectors)
      http.go                   HTTP metrics middleware (request counts, latency, in-flight)
      vote.go                   Vote processing metrics (processed total, duration, by target)
      cache.go                  Config cache metrics (hits/misses by layer, invalidations)
      websocket.go              WebSocket metrics (active connections, messages published)
    eventpublisher/             Event publisher (composes WebSocket + Redis cache invalidation)
      publisher.go              Implements domain.EventPublisher
  platform/                     Cross-cutting infrastructure (stdlib only)
    config/config.go            Struct-tag-based env loading, validation (all required fields enforced, production SSL check)
    correlation/correlation.go  Correlation ID generation (8-char hex), context propagation, slog handler wrapper
    crypto/crypto.go            crypto.Service interface, AesGcmCryptoService (AES-256-GCM)
    crypto/cryptotest/noop.go   NoopCryptoService for testing
    errors/errors.go            Structured error types (validation, not_found, conflict, internal, external), HTTP status mapping
    logging/logger.go           Structured logging setup (slog, configurable level + format)
    retry/retry.go              Exponential backoff with jitter, classification (Stop/Retry/After)
    version/version.go          Build version info (ldflags)
web/
  templates.go                  Embedded templates (embed.FS)
  templates/
    landing.html                Marketing landing page with animated demo bar
    login.html                  Login page with Twitch OAuth button
    dashboard.html              Streamer config UI
    overlay.html                OBS overlay (lerp-based rendering, combined tug-of-war + split bars)
```

## Key Routes

```
GET  /                          Landing page (redirects to /dashboard if authenticated)
GET  /metrics                   Prometheus metrics endpoint (optional, when metrics enabled)
GET  /health/startup            Startup probe (2s timeout)
GET  /health/live               Liveness probe (uptime)
GET  /health/ready              Readiness probe (Redis, PostgreSQL, 5s timeout)
GET  /version                   Build info (version, commit, build time, Go version)
GET  /auth/login                Login page with Twitch OAuth button
GET  /auth/callback             Twitch OAuth callback
POST /auth/logout               Clear session, redirect to login (auth + CSRF)
GET  /dashboard                 Streamer config page (auth + CSRF)
POST /dashboard/config          Save config (auth + CSRF)
POST /api/reset/:uuid           Reset sentiment bar (auth + CSRF)
POST /api/rotate-overlay-uuid   Generate new overlay URL (auth + CSRF)
GET  /api/overlay-status        Get overlay viewer count (auth + CSRF)
GET  /overlay/:uuid             Serve overlay page (public)
GET  /connection/websocket      Centrifuge WebSocket endpoint (public, auth via overlay UUID query param)
POST /webhooks/eventsub         Twitch EventSub webhook receiver (HMAC verified)
```

## Environment Variables

See `.env.example` for all variables with comments.

**Required**:
- `DATABASE_URL` — PostgreSQL connection string
- `REDIS_URL` — Redis connection string
- `TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET` — Twitch app credentials
- `TWITCH_REDIRECT_URI` — OAuth callback URL
- `SESSION_SECRET` — HMAC secret for session cookies
- `TOKEN_ENCRYPTION_KEY` — 64 hex chars (32 bytes) for AES-256-GCM token encryption
- `WEBHOOK_CALLBACK_URL` — Public HTTPS URL for EventSub delivery
- `WEBHOOK_SECRET` — HMAC secret for webhook verification (10-100 chars)
- `BOT_USER_ID` — Twitch user ID of the dedicated bot account

**Optional** (with defaults):
- `APP_ENV` — `development` or `production` (controls secure cookies)
- `PORT` — Server port (default: `8080`)
- `LOG_LEVEL` — `debug`, `info`, `warn`, `error` (default: `info`)
- `LOG_FORMAT` — `text` or `json` (default: `text`)
- `MAX_WEBSOCKET_CONNECTIONS` — File descriptor limit check at startup (default: `10000`)
- `SESSION_MAX_AGE` — Cookie expiry duration (default: `168h` / 7 days)
- `SHUTDOWN_TIMEOUT` — Graceful shutdown deadline (default: `10s`)

## Architecture

### Scaling Model

Redis-centric session state. Redis Streams store votes with sliding-window expiry. Multiple instances share state through Redis. Centrifuge handles WebSocket fan-out with Redis broker for cross-instance broadcasting.

### Data Consistency

**PostgreSQL = source of truth** (user config with `version` field for optimistic locking). **Redis = session cache**. Eventual consistency via 3-layer read-through cache + pub/sub invalidation. Config saves publish invalidation to all instances; each evicts local in-memory cache + Redis cache. Failure mode: TTL expiry fixes staleness (in-memory: 10s, Redis: 1h).

### Bot Account

Single dedicated bot reads chat for all streamers. Bot authorizes `user:read:chat` + `user:bot`; streamers grant `channel:bot` during OAuth. EventSub subscriptions: `user_id` = bot, `broadcaster_user_id` = streamer.

### EventSub: Webhooks + Conduits

- **EventSubManager** (`adapter/twitch/eventsub.go`): Owns `*helix.Client` (Kappopher). Validates credentials via `GetAppAccessToken`. Manages conduit lifecycle and subscriptions with retry (exponential backoff, 429 rate limit handling). Subscriptions persisted in PostgreSQL. 409 Conflict = idempotent success.
- **WebhookHandler** (`adapter/twitch/webhook.go`): HMAC-SHA256 verification (Kappopher), timestamp freshness validation (10-min window). Skips vote processing when no viewers connected. Processes votes via `overlay.ProcessMessage()` with 5s timeout.

### Vote Processing Pipeline

1. Twitch `channel.chat.message` webhook → HMAC verification + timestamp check
2. `hasViewers` check (Centrifuge presence) → skip if no viewers watching
3. `getConfig()` → config (3-layer cache: in-memory → Redis → PostgreSQL)
4. `matchTrigger()` → `VoteTarget` (positive, negative, or none) — case-insensitive exact match
5. `debouncer.IsDebounced()` → 1s per user (SETNX, fail-open on errors)
6. `sentimentStore.RecordVote()` → Redis Streams pipeline (XADD + XTRIM MINID + XRANGE) → `WindowSnapshot`
7. Centrifuge pub/sub → fan out to WebSocket clients

### Sentiment Model

Sliding-window vote counting. Each vote is recorded as a Redis Stream entry; sentiment is computed as `forCount / totalVotes` and `againstCount / totalVotes` over a configurable time window (`memory_seconds`, 5–120s, default 30). Old votes naturally fall out of the window — no decay math needed.

**WindowSnapshot**: `{ForRatio float64, AgainstRatio float64, TotalVotes int}` — server-computed ratios sent to clients.

Display modes:
- **Combined** (tug-of-war): bar position = `forRatio - againstRatio` mapped to [-1, 1] range
- **Split** (two bars): each bar fills to `forRatio * 100%` and `againstRatio * 100%`

**Client-side lerp**: Overlay smoothly animates toward server ratios using `current += (target - current) * 0.15` per `requestAnimationFrame` — zero server cost for smooth visuals.

### Redis Architecture

**Keys**: `votes:{broadcasterID}` (Redis Stream, 10-min TTL), `debounce:{broadcasterID}:{userID}` (1s TTL), config cache keys (1h TTL)

**Redis Streams**: Each vote is an XADD entry with `vote +1` or `vote -1`. XTRIM MINID trims entries outside the sliding window. XRANGE reads the window for ratio computation. All three run in a single pipeline per vote.

**Pub/Sub**: `sentiment:changes` (vote updates via Centrifuge Redis broker), `config:invalidate` (cache invalidation across instances)

### WebSocket (Centrifuge)

- Overlay UUID → Twitch User ID resolution via repository lookup
- Auto-subscribe to `sentiment:{twitchUserID}` channel on connect
- Presence stats via Centrifuge PresenceManager (used for viewer count / hasViewers check)
- Redis broker for cross-instance message delivery
- Origin validation: allows empty, `obs://`, app origin, localhost (dev only)
- Publishes: `{forRatio, againstRatio, totalVotes, displayMode, status}`

### Security

- **Rate limiting**: Per-IP via Echo middleware — auth ~10 req/min (burst 5), dashboard/API ~30 req/min (burst 10), webhooks ~200 req/min (burst 50)
- **Security headers**: X-Frame-Options: DENY, HSTS (2y, preload), Content-Security-Policy, X-Content-Type-Options: nosniff, Referrer-Policy: strict-origin-when-cross-origin
- **WebSocket origin validation**: Prevents cross-site WebSocket hijacking (`websocket/origin.go`)
- **Production SSL enforcement**: Config validation rejects `sslmode=disable` or `sslmode=allow` in `DATABASE_URL` when `APP_ENV=production`
- **Non-root Docker**: Dockerfile uses dedicated non-root user

## Dependency Rules

```
Domain ← Platform ← App ← Adapter ← Main
```

- **Domain** (`domain/`) → stdlib + `uuid` only (no internal deps)
- **Platform** (`platform/`) → stdlib only (no internal deps)
- **App** (`app/`) → Domain + Platform
- **Adapter** (`adapter/`) → Domain + Platform + App + external libs
- **Main** (`cmd/`) → everything (wiring only)
- **Forbidden**: Domain → anything internal; Platform → Domain

## Testing

```bash
make test-short     # Unit tests only (<2s, no Docker)
make test           # All tests (unit + integration, ~15s)
make test-unit      # Alias for test-short
make test-integration  # Integration tests only (~12s, requires Docker)
make test-race      # With race detector (short mode)
make test-coverage  # Coverage report (coverage.out + coverage.html)
```

**Unit tests**: `<feature>_test.go` — mocks for all external dependencies.
**Integration tests**: `<feature>_integration_test.go` — testcontainers for PostgreSQL and Redis. `TestMain()` starts containers once, cleanup via `TRUNCATE`/`FlushAll`.

**Testcontainers with Rancher Desktop** requires:
```bash
export DOCKER_HOST=unix://$HOME/.rd/docker.sock
export TESTCONTAINERS_RYUK_DISABLED=true
```

## Observability

**Structured logging** via `slog` (configurable level and format via `LOG_LEVEL` / `LOG_FORMAT`). Centrifuge WebSocket logging also respects `LOG_LEVEL`.

**Correlation IDs**: Every HTTP request gets an 8-char hex correlation ID (`platform/correlation/`). The correlation slog handler automatically injects `correlation_id` into all log entries for request tracing.

**Audit logging**: Sensitive operations emit `slog.InfoContext` audit logs: login, logout, config save, sentiment reset, overlay UUID rotation.

**Prometheus metrics** (`GET /metrics`): HTTP request counts/latency/in-flight, vote processing (totals, duration, by target), config cache hits/misses/invalidations, WebSocket active connections/messages published. Skips `/metrics` and `/health/*` from HTTP metrics.

**Build info** via `GET /version` endpoint (ldflags-injected version, commit, build time).

## Linting

golangci-lint v2 via `.golangci.yml`. Run with `make lint`. Generated code (`sqlcgen/`) is excluded.

## Go Module

`github.com/pscheid92/chatpulse`, Go 1.26.0

**Requires PostgreSQL 18+** (uses `uuidv7()` for time-ordered UUIDs).

**Key deps**: echo/v4, centrifuge (WebSocket), gorilla/sessions, kappopher (Twitch Helix), pgx/v5, tern/v2, go-redis/v9, prometheus/client_golang, testcontainers-go, go-simpler/env
