# ADR-003: Twitch EventSub webhooks + conduits vs WebSocket

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse needs to receive chat messages from Twitch to analyze sentiment (match trigger words like "Pog" / "ResidentSleeper"). Twitch provides EventSub, their event subscription API, with two transport options:

1. **EventSub WebSocket:** Our app opens a WebSocket connection to Twitch. Twitch pushes events over that connection.
2. **EventSub Webhooks:** Twitch POSTs events to our HTTPS endpoint. We verify HMAC signatures and process the events.

A previous version of this codebase used EventSub WebSocket with a complex 5-state FSM (connecting → connected → ready → reconnecting → error states). The code was 660 lines and difficult to maintain.

Additionally, Twitch introduced **conduits** - a feature that allows the webhook URL to change without re-subscribing to all events.

## Decision

**Use EventSub webhooks via a conduit for receiving chat messages. Twitch POSTs to our `/webhooks/eventsub` endpoint.**

Specifically:

- **Transport:** HTTP webhooks (Twitch POSTs JSON to our public HTTPS endpoint)
- **Conduit:** Single conduit shared across all streamers (created on startup, reused if it already exists)
- **Webhook verification:** Use Kappopher's built-in HMAC-SHA256 signature verification (compares `Twitch-Eventsub-Message-Signature` header)
- **Configuration:** Requires three environment variables:
  - `WEBHOOK_CALLBACK_URL` - Public HTTPS URL for Twitch to POST to (e.g., `https://chatpulse.example.com/webhooks/eventsub`)
  - `WEBHOOK_SECRET` - HMAC secret for signature verification (10-100 chars)
  - `BOT_USER_ID` - Twitch user ID of the bot account that reads chat
- **Startup logic:** On app start, `EventSubManager.Setup()` finds or creates a conduit, then configures a webhook shard pointing to `WEBHOOK_CALLBACK_URL`
- **Shutdown logic:** On app shutdown, `EventSubManager.Cleanup()` deletes the conduit (Twitch automatically deletes associated subscriptions)

**Code structure:**
```go
// EventSubManager wraps Kappopher's *helix.Client directly
type EventSubManager struct {
    client        *helix.Client  // Kappopher client (handles auth, retries, HMAC)
    conduitID     string
    webhookSecret string
    botUserID     string
    // ...
}

// Webhook handler (twitch/webhook.go)
func (h *WebhookHandler) HandleEventSub(c echo.Context) error {
    // Kappopher verifies HMAC signature automatically
    event := helix.ParseWebhookEvent(c.Request())

    if event.Type == "channel.chat.message" {
        // Process vote via engine.ProcessVote()
    }
    return c.NoContent(200)
}
```

## Alternatives Considered

### 1. EventSub WebSocket (long-lived client connection)

**Description:** Open a WebSocket connection from our app to Twitch (`wss://eventsub.wss.twitch.tv/ws`). Twitch pushes events over this connection. Requires handling keepalive messages, reconnects, session resumption, and welcome/reconnect message sequencing.

**Rejected because:**
- **660 lines of complex FSM code:** Previous implementation had 5 states (connecting, connected, ready, reconnecting, error) with 15+ state transitions. Very hard to reason about correctness.
- **Connection management complexity:** Must send keepalive pings, handle reconnect messages (which tell us to open a *new* connection while keeping the old one alive temporarily), and implement session resumption to avoid missing events during reconnects.
- **Harder to horizontally scale:** WebSocket connections have affinity - they stay connected to one instance. If that instance crashes, we need to detect the failure and reconnect from another instance. With webhooks, any instance can handle any event.
- **Debugging is harder:** When events are missing, is it because the WebSocket disconnected? Is the keepalive working? Did the reconnect fail? Webhooks are stateless HTTP requests (easier to debug with curl).
- **Previous implementation was deleted:** After the February 2026 refactor, the WebSocket FSM code was removed (~660 lines deleted). The team explicitly chose not to maintain it.

**Why we initially used WebSocket:**
- Seemed simpler in theory ("just open a connection and receive events")
- Didn't require public HTTPS endpoint (could run localhost during development)
- Reality: reconnect logic and state management made it very complex in practice

### 2. Twitch IRC (chat.twitch.tv) - Legacy chat protocol

**Description:** Connect to Twitch IRC servers using the IRC protocol (like mIRC, HexChat, etc.). This is the "old way" of reading Twitch chat.

**Rejected because:**
- **Deprecated by Twitch:** Twitch explicitly recommends EventSub for new integrations. IRC is in maintenance mode.
- **Fewer guarantees:** IRC is best-effort. Messages can be dropped during high traffic. EventSub has better reliability guarantees.
- **No official Go SDK:** Would need to use a third-party IRC library or write our own IRC parser.
- **Limited to chat messages:** Can't subscribe to other EventSub event types (raids, subscriptions, etc.) if we want to add features later.

### 3. Polling Twitch API (fetch recent messages periodically)

**Description:** Use Twitch's "Get Chatters" or similar API to poll for new messages every 1-5 seconds.

**Rejected because:**
- **High latency:** Polling every 5 seconds means sentiment updates lag by up to 5 seconds. EventSub delivers events within milliseconds.
- **Rate limit concerns:** Twitch API has rate limits (800 requests per minute for most endpoints). Polling hundreds of channels would quickly exceed limits.
- **Inefficient:** Most polls would return "no new messages" (wasted API calls). EventSub only sends events when something actually happens.
- **No API for real-time chat messages:** Twitch doesn't expose a "get recent messages" endpoint. The IRC/EventSub is the intended way.

### 4. Webhooks without conduit (direct subscription per streamer)

**Description:** Use webhooks but skip the conduit - subscribe directly to each streamer's channel with our webhook URL.

**Rejected because:**
- **Can't change webhook URL without re-subscribing:** If we need to change the webhook URL (e.g., new domain, load balancer change), we'd have to delete and recreate *every* subscription. With conduits, we just update the shard URL.
- **More subscriptions to manage:** Without conduits, we'd have one subscription per streamer per event type. With conduits, all subscriptions go to the conduit, and the conduit fans out to our webhook.
- **Conduits are the recommended approach:** Twitch's docs explicitly recommend conduits for multi-tenant apps like ChatPulse.

## Consequences

### ✅ Positive

- **Simpler deployment:** Standard HTTP request handling. No long-lived connection management, no reconnect state machines, no keepalive logic.
- **Familiar patterns:** Echo route handlers, middleware, logging - same patterns as the rest of the HTTP API.
- **Kappopher handles HMAC verification:** Built-in `helix.ParseWebhookEvent()` verifies the `Twitch-Eventsub-Message-Signature` header automatically. Less code for us to write and test.
- **Horizontal scaling easier:** Any instance can handle any webhook POST. No connection affinity. Load balancer just does round-robin HTTP.
- **Conduit allows URL changes:** If we change the webhook URL (e.g., new domain), we update the conduit shard - no need to re-subscribe to hundreds of channels.
- **Stateless:** Each webhook POST is independent. No session state to track between requests.
- **Easier debugging:** Can test with curl or Postman. Can inspect webhook POSTs in logs. Can replay events by re-sending the HTTP request.

**Code reduction:**
- EventSub WebSocket FSM: **~660 lines deleted**
- EventSub webhook handler: **~150 lines added**
- **Net reduction: 510 lines (77% less code)**

### ❌ Negative

- **Requires public HTTPS endpoint:** Can't run purely on localhost without a tunnel (ngrok, localtunnel, etc.). Development setup is slightly more complex.
- **Must configure webhook environment variables:** Requires `WEBHOOK_CALLBACK_URL`, `WEBHOOK_SECRET`, `BOT_USER_ID`. Can't start the app without these (will fail fast with error message).
- **Twitch must reach the app:** Firewall rules, NAT, reverse proxy must all be configured correctly. If Twitch can't POST to the endpoint, events are silently dropped (Twitch will retry, but eventually gives up).
- **HMAC secret management:** The webhook secret must be kept secure. If leaked, attackers could forge webhook events. Must rotate if compromised.

### 🔄 Trade-offs

- **Chose operational simplicity over local-only development:** We accept the requirement for a public HTTPS endpoint because it massively simplifies the code. Developers can use ngrok or deploy to a test environment.
- **Accept configuration overhead for less code:** The three environment variables (`WEBHOOK_CALLBACK_URL`, `WEBHOOK_SECRET`, `BOT_USER_ID`) add configuration burden, but eliminate 660 lines of complex state machine code.
- **Reduced from 660 lines to ~150 lines:** By switching from WebSocket FSM to webhooks, we cut EventSub transport code by 77%. This is a huge maintenance win.

## Related Decisions

- **ADR-004: Single bot account architecture** (future) - Consequence: the bot's `user_id` is used in EventSub subscriptions. The `BOT_USER_ID` env var provides this value.
- **ADR-001: Redis-only architecture** - Context: webhooks are stateless, which fits well with the stateless instance model from ADR-001.

## Migration Notes

This decision represents a **reversal** from the original WebSocket implementation. The WebSocket code was removed in the February 2026 refactor after identifying that:

1. The 5-state FSM was too complex to maintain reliably
2. Reconnect logic was buggy (missed events during transitions)
3. Horizontal scaling required sticky WebSocket connections (not compatible with ADR-001)
4. Twitch's conduit feature made webhooks much more practical (can change URL without re-subscribing)

**No migration path is needed** - the old WebSocket code was deleted entirely. This ADR documents the new approach.

## Implementation References

- `internal/twitch/eventsub.go` - EventSubManager (conduit lifecycle, subscription management)
- `internal/twitch/webhook.go` - WebhookHandler (HMAC verification, event processing)
- `cmd/server/main.go` - `initWebhooks()` (conditional setup based on env vars)
- `.env.example` - Webhook configuration documentation
