# ChatPulse

A real-time chat sentiment tracking overlay for Twitch streamers. Monitor your chat's mood during polls, debates, or Q&A sessions with a glassmorphism overlay for OBS.

A dedicated bot account reads chat on behalf of all streamers, making this a multi-tenant SaaS that supports horizontal scaling via Redis.

## Features

- **Real-time sentiment tracking** from Twitch chat messages via EventSub webhooks
- **Customizable triggers** for "for" and "against" votes
- **Glassmorphism overlay** for OBS browser sources
- **Configurable decay speed** to smoothly return the bar to center
- **Multi-instance scaling** with Redis (optional) for horizontal deployment
- **Bot account architecture** — streamers only grant `channel:bot` scope; a single bot reads all channels
- **Token encryption at rest** with AES-256-GCM (optional)
- **Overlay URL rotation** to invalidate old URLs

## Prerequisites

1. **Twitch Application**: Register at https://dev.twitch.tv/console/apps
   - Get your Client ID and Client Secret
   - Set the OAuth Redirect URL to `http://localhost:8080/auth/callback` (or your production domain)

2. **Twitch Bot Account**: A dedicated Twitch account that will read chat
   - Authorize the bot with `user:read:chat` and `user:bot` scopes (one-time setup)
   - Note the bot's Twitch user ID for `BOT_USER_ID`

3. **PostgreSQL**: Version 15 or higher

4. **Public HTTPS URL**: Required for EventSub webhook delivery (use [ngrok](https://ngrok.com/) for local development)

5. **Go**: Version 1.25+ (for local development only)

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

2. Start PostgreSQL (or use Docker):
```bash
docker run -d \
  --name chatpulse-postgres \
  -e POSTGRES_USER=twitchuser \
  -e POSTGRES_PASSWORD=twitchpass \
  -e POSTGRES_DB=twitchdb \
  -p 5432:5432 \
  postgres:15-alpine
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
make build          # Build binary -> ./server
make run            # Build and run locally
make test              # Run all tests (unit + integration, ~15s)
make test-short        # Run unit tests only (fast, <2s, no Docker)
make test-unit         # Alias for test-short
make test-integration  # Run integration tests only (~12s, requires Docker)
make test-race         # Run tests with race detector
make test-coverage     # Generate coverage report
make fmt            # Format code
make lint           # Run golangci-lint
make deps           # Download and tidy dependencies
make docker-up      # Start with Docker Compose (app + PostgreSQL + Redis)
make docker-down    # Stop Docker Compose
make clean          # Remove build artifacts
```

## Usage

### 1. Configure Your Overlay

1. Visit `http://localhost:8080` and log in with your Twitch account
2. Configure your sentiment triggers:
   - **For Trigger**: Word/phrase viewers type to vote "for" (e.g., "yes", "agree")
   - **Against Trigger**: Word/phrase to vote "against" (e.g., "no", "disagree")
   - **Left/Right Labels**: Display labels for each side
   - **Decay Speed**: How quickly the bar returns to center (0.1 = slow, 2.0 = fast)
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

See `.env.example` for all variables.

**Required**:
- `DATABASE_URL` — PostgreSQL connection string
- `TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET` — Twitch app credentials
- `TWITCH_REDIRECT_URI` — OAuth callback URL
- `SESSION_SECRET` — Secret for session cookies

**Webhook** (all three required together):
- `WEBHOOK_CALLBACK_URL` — Public HTTPS URL for EventSub webhook delivery
- `WEBHOOK_SECRET` — HMAC secret for webhook verification (10-100 chars)
- `BOT_USER_ID` — Twitch user ID of the bot account

**Optional**:
- `TOKEN_ENCRYPTION_KEY` — 64 hex chars for AES-256-GCM token encryption at rest
- `REDIS_URL` — Enables multi-instance mode (e.g., `redis://localhost:6379`)

## How It Works

- **Bot Account**: A single bot account reads chat in all connected channels via EventSub webhooks
- **Webhooks + Conduits**: Chat messages arrive via Twitch EventSub webhooks transported through a Conduit, verified with HMAC-SHA256
- **Vote Processing**: Messages containing trigger words are counted as votes (case-insensitive substring match, "for" takes priority)
- **Debouncing**: Each viewer can vote once per second to prevent spam
- **Decay**: The sentiment bar gradually returns to center (50ms tick interval)
- **Real-time Broadcast**: Updates are pushed to overlay clients via WebSocket

## Architecture

- **Backend**: Single Go binary (Echo v4) serving HTTP, WebSocket, and webhook endpoints
- **Database**: PostgreSQL 15+ with auto-migrations for users, configs, and EventSub subscriptions
- **Scaling**: Single-instance (in-memory) or multi-instance (Redis with Lua scripts for atomic operations and Pub/Sub for cross-instance broadcasting)
- **Concurrency**: Actor pattern for the Sentiment Engine and WebSocket Hub — no mutexes on actor-owned state
- **Frontend**: Minimal HTML/CSS/JS with no external dependencies

## Production Deployment

1. Use HTTPS with a reverse proxy (nginx/Caddy) for SSL termination
2. Set `TWITCH_REDIRECT_URI` to your production domain
3. Generate strong secrets:
   ```bash
   SESSION_SECRET=$(openssl rand -hex 32)
   WEBHOOK_SECRET=$(openssl rand -hex 16)
   TOKEN_ENCRYPTION_KEY=$(openssl rand -hex 32)
   ```
4. Set `APP_ENV=production` for secure cookies
5. Optionally set `REDIS_URL` for multi-instance scaling
6. Configure PostgreSQL backups

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
