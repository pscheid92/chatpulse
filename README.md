# ChatPulse

[![Tests](https://github.com/pscheid92/chatpulse/actions/workflows/test.yml/badge.svg)](https://github.com/pscheid92/chatpulse/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/pscheid92/chatpulse)](https://goreportcard.com/report/github.com/pscheid92/chatpulse)

A real-time chat sentiment tracking overlay for Twitch streamers. Monitor your chat's mood during polls, debates, or Q&A sessions with a glassmorphism overlay for OBS.

A dedicated bot account reads chat on behalf of all streamers, making this a multi-tenant SaaS that supports horizontal scaling via Redis.

## Features

- **Real-time sentiment tracking** from Twitch chat messages via EventSub webhooks
- **Two display modes**: combined tug-of-war bar or split positive/negative bars
- **Customizable triggers** and labels for "for" and "against" votes
- **Sliding-window memory** (5-120s) controls how long votes count
- **Multi-instance scaling** with Redis for horizontal deployment
- **Bot account architecture** — streamers only grant `channel:bot` scope; a single bot reads all channels
- **Token encryption at rest** with AES-256-GCM
- **Overlay URL rotation** to invalidate old URLs
- **Per-user debouncing** (1s) to prevent spam
- **Zero-cost idle** — skips processing when no overlay viewers are connected
- **Prometheus metrics** for HTTP, vote processing, cache, and WebSocket
- **Correlation IDs** for end-to-end request tracing
- **Audit logging** for sensitive operations (login, config changes, UUID rotation)
- **Per-IP rate limiting** on all route groups
- **Security headers** (HSTS, CSP, X-Frame-Options, etc.)

## Prerequisites

1. **Twitch Application**: Register at https://dev.twitch.tv/console/apps
   - Get your Client ID and Client Secret
   - Set the OAuth Redirect URL to `http://localhost:8080/auth/callback` (or your production domain)

2. **Twitch Bot Account**: A dedicated Twitch account that will read chat
   - Authorize the bot with `user:read:chat` and `user:bot` scopes (one-time setup)
   - Note the bot's Twitch user ID for `BOT_USER_ID`

3. **PostgreSQL**: Version 18 or higher (required for `uuidv7()`)

4. **Redis**: Version 7+

5. **Public HTTPS URL**: Required for EventSub webhook delivery (use [ngrok](https://ngrok.com/) for local development)

6. **Go**: Version 1.26+ (for local development only)

## Quick Start with Docker

1. Clone the repository:
```bash
git clone https://github.com/pscheid92/chatpulse.git
cd chatpulse
```

2. Create a `.env` file from the example:
```bash
cp .env.example .env
```

3. Edit `.env` with your credentials:
```bash
TWITCH_CLIENT_ID=your_client_id
TWITCH_CLIENT_SECRET=your_client_secret
TWITCH_REDIRECT_URI=http://localhost:8080/auth/callback
SESSION_SECRET=$(openssl rand -hex 32)
TOKEN_ENCRYPTION_KEY=$(openssl rand -hex 32)
WEBHOOK_CALLBACK_URL=https://your-subdomain.ngrok-free.app/webhooks/eventsub
WEBHOOK_SECRET=$(openssl rand -hex 16)
BOT_USER_ID=your_bot_twitch_user_id
```

4. Start the application:
```bash
make docker-up
```

5. Open `http://localhost:8080` in your browser.

## Local Development

1. Install dependencies:
```bash
make deps
```

2. Start PostgreSQL and Redis (or use Docker):
```bash
docker run -d \
  --name chatpulse-postgres \
  -e POSTGRES_USER=twitchuser \
  -e POSTGRES_PASSWORD=twitchpass \
  -e POSTGRES_DB=twitchdb \
  -p 5432:5432 \
  postgres:18-alpine

docker run -d \
  --name chatpulse-redis \
  -p 6379:6379 \
  redis:8-alpine
```

3. Expose your local server for webhooks:
```bash
ngrok http 8080
```

4. Set up your `.env` file as described above (use the ngrok URL for `WEBHOOK_CALLBACK_URL`).

5. Run the server:
```bash
make run
```

### Make Targets

```
make build             # Build binary -> ./server
make run               # Build and run locally
make test              # Run all tests (unit + integration, ~15s)
make test-short        # Run unit tests only (fast, <2s, no Docker)
make test-unit         # Alias for test-short
make test-integration  # Run integration tests only (~12s, requires Docker)
make test-race         # Run tests with race detector
make test-coverage     # Generate coverage report
make fmt               # Format code
make lint              # Run golangci-lint
make deps              # Download and tidy dependencies
make sqlc              # Regenerate sqlc code
make docker-build      # Build Docker image
make docker-up         # Start with Docker Compose (app + PostgreSQL 18 + Redis 8)
make docker-down       # Stop Docker Compose
make clean             # Remove build artifacts
```

## Testing

```bash
make test           # Run all tests (unit + integration, ~15s)
make test-short     # Run unit tests only (skip integration, <2s)
make test-race      # Run with race detector
make test-coverage  # Generate coverage report
```

**TDD workflow:**
- Use `make test-short` for rapid feedback during development (<2s, no Docker)
- Run `make test` before committing (full suite with testcontainers)
- CI runs full suite on every push

## Usage

### 1. Configure Your Overlay

1. Visit `http://localhost:8080` and log in with your Twitch account
2. Configure your sentiment triggers:
   - **For Trigger**: Word/phrase viewers type to vote "for" (e.g., "yes", "agree")
   - **Against Trigger**: Word/phrase to vote "against" (e.g., "no", "disagree")
   - **Labels**: Display labels for each side
   - **Memory**: How long votes count in the sliding window (5-120s, or infinite)
   - **Display Mode**: Combined (tug-of-war) or Split (two bars)
3. Click "Save Configuration"

### 2. Add to OBS

1. Copy your unique overlay URL from the dashboard
2. In OBS, add a new **Browser Source**
3. Paste your overlay URL
4. Set dimensions: 800x100 (adjust to preference)

### 3. During Your Stream

- Use the **Reset to Center** button on the dashboard to reset the sentiment bar
- Use **Rotate Overlay URL** to generate a new URL and invalidate the old one

## Environment Variables

See `.env.example` for all variables with comments.

**Required**:
- `DATABASE_URL` — PostgreSQL connection string
- `REDIS_URL` — Redis connection string (e.g., `redis://localhost:6379`)
- `TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET` — Twitch app credentials
- `TWITCH_REDIRECT_URI` — OAuth callback URL
- `SESSION_SECRET` — Secret for session cookies
- `TOKEN_ENCRYPTION_KEY` — 64 hex chars for AES-256-GCM token encryption (generate: `openssl rand -hex 32`)
- `WEBHOOK_CALLBACK_URL` — Public HTTPS URL for EventSub webhook delivery
- `WEBHOOK_SECRET` — HMAC secret for webhook verification (10-100 chars)
- `BOT_USER_ID` — Twitch user ID of the bot account

**Optional**:
- `APP_ENV` — `development` (default) or `production` (controls secure cookies)
- `PORT` — Server port (default: `8080`)
- `LOG_LEVEL` — `debug`, `info`, `warn`, `error` (default: `info`)
- `LOG_FORMAT` — `text` (default) or `json` (recommended for production)
- `MAX_WEBSOCKET_CONNECTIONS` — File descriptor limit check (default: `10000`)
- `SESSION_MAX_AGE` — Cookie expiry (default: `168h` / 7 days)
- `SHUTDOWN_TIMEOUT` — Graceful shutdown deadline (default: `10s`)

## How It Works

- **Bot Account**: A single bot account reads chat in all connected channels via EventSub webhooks
- **Webhooks + Conduits**: Chat messages arrive via Twitch EventSub webhooks transported through a Conduit, verified with HMAC-SHA256
- **Vote Processing**: Messages matching trigger words exactly (case-insensitive) are counted as votes
- **Debouncing**: Each viewer can vote once per second to prevent spam
- **Sliding-window Counting**: Votes are recorded in Redis Streams; sentiment is computed over a configurable time window (old votes naturally expire)
- **Real-time Broadcast**: Updates are pushed to overlay clients via Centrifuge WebSocket with Redis broker for cross-instance delivery
- **Client-side Lerp**: The overlay uses `requestAnimationFrame` for smooth animation toward server ratios with zero server cost
- **Rate Limiting**: Per-IP rate limits on auth, dashboard/API, and webhook routes
- **Correlation IDs**: Every request gets a unique ID propagated through logs for tracing
- **Audit Logging**: Sensitive operations (login, logout, config save, reset, URL rotation) emit structured audit logs

## Architecture

- **Backend**: Single Go binary (Echo v4) serving HTTP, WebSocket, and webhook endpoints
- **Database**: PostgreSQL 18+ with auto-migrations (tern) for streamers, configs, and EventSub subscriptions
- **Caching**: 3-layer read-through cache (in-memory 10s → Redis 1h → PostgreSQL) with pub/sub invalidation
- **Scaling**: Multi-instance via Redis (Streams for vote counting, pub/sub for broadcasting, Centrifuge Redis broker for WebSocket fan-out)
- **Observability**: Structured logging (slog) with correlation IDs, audit logs, Prometheus metrics (`/metrics` endpoint)
- **Security**: Per-IP rate limiting, security headers (HSTS, CSP), WebSocket origin validation, production SSL enforcement
- **Frontend**: Minimal HTML/CSS/JS with no external dependencies, embedded via `go:embed`

## Production Deployment

1. Use HTTPS with a reverse proxy (nginx/Caddy) for SSL termination
2. Set `TWITCH_REDIRECT_URI` to your production domain
3. Generate strong secrets:
   ```bash
   SESSION_SECRET=$(openssl rand -hex 32)
   WEBHOOK_SECRET=$(openssl rand -hex 16)
   TOKEN_ENCRYPTION_KEY=$(openssl rand -hex 32)
   ```
4. Set `APP_ENV=production` for secure cookies (also enforces `DATABASE_URL` SSL — rejects `sslmode=disable/allow`)
5. Set `LOG_FORMAT=json` for structured logging
6. Configure PostgreSQL backups
7. Prometheus metrics are available at `/metrics` for monitoring

## Troubleshooting

### Overlay not connecting?
- Verify the server is running and the overlay URL is correct
- Check browser console for WebSocket errors

### Chat messages not being tracked?
- Ensure webhook delivery is working (check server logs for EventSub notifications)
- Verify the bot account has authorized `user:read:chat` + `user:bot` scopes
- Confirm `BOT_USER_ID` matches the bot's Twitch user ID
- Check that `WEBHOOK_CALLBACK_URL` is publicly reachable over HTTPS

### Webhooks not arriving?
- For local dev, ensure ngrok is running and the URL in `.env` matches
- Check that `WEBHOOK_SECRET` is at least 10 characters

## License

MIT License - see LICENSE file for details.
