# ADR-004: Single bot account reads all channels

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse needs to read chat messages from every streamer's Twitch channel to analyze sentiment. Twitch's EventSub API requires user authorization to subscribe to chat message events. There are two authorization models to consider:

1. **Per-streamer authorization:** Each streamer authorizes the ChatPulse app with full chat read permissions
2. **Bot account model:** A dedicated "bot" account is authorized once, and streamers grant permission for the bot to read their chat

Twitch introduced specialized scopes for the bot model:
- `user:read:chat` - Bot can read chat messages
- `user:bot` - Bot can act as a bot user
- `channel:bot` - Streamer allows a specific bot into their channel

The question: Should each streamer authorize full chat access, or should we use a single bot account?

## Decision

**Use a single dedicated bot account to read chat on behalf of all streamers.**

Specifically:

- **Bot account setup (one-time):** The ChatPulse operator creates a dedicated Twitch account (e.g., `chatpulse_bot`) and authorizes it with `user:read:chat` + `user:bot` scopes. This happens once during initial deployment.
- **Streamer authorization:** When a streamer logs into ChatPulse, they only grant the `channel:bot` scope, which allows the bot account to read their channel's chat. They don't grant chat reading permissions directly to the app.
- **EventSub subscriptions:** When subscribing to `channel.chat.message` events, we set:
  - `user_id` = bot's Twitch user ID (the bot is reading)
  - `broadcaster_user_id` = streamer's Twitch user ID (reading this channel)
- **Configuration:** The bot's Twitch user ID is provided via the `BOT_USER_ID` environment variable. The app validates this on startup.

**Authentication flow:**
```
Streamer → OAuth login → Grant "channel:bot" scope → ChatPulse
ChatPulse → EventSub subscribe → user_id=BOT_USER_ID, broadcaster_user_id=STREAMER_ID → Twitch
Twitch → Sends chat messages to ChatPulse webhook → Bot reads on behalf of streamer
```

**Code references:**
- `BOT_USER_ID` env var validated in `config/config.go`
- Bot user ID stored in `EventSubManager` and used in `CreateEventSubSubscription()`
- Streamer OAuth only requests `channel:bot` scope (see OAuth client configuration)

## Alternatives Considered

### 1. Per-streamer OAuth (each streamer authorizes full chat access)

**Description:** When a streamer logs into ChatPulse, they authorize the app with `user:read:chat` scope directly. ChatPulse subscribes to EventSub with the streamer as the `user_id`.

**Rejected because:**
- **More complex OAuth flow:** Streamers must approve more scopes (`user:read:chat` is broader than just `channel:bot`). This increases friction during onboarding.
- **Token management per streamer:** Each streamer's OAuth token must be stored, refreshed on expiry, and monitored. If a token expires and refresh fails, that streamer's chat goes dark.
- **EventSub subscription complexity:** One EventSub subscription per streamer, each with a different `user_id`. More subscriptions to track and manage.
- **Less clear permission model:** Streamers might wonder "why does ChatPulse need to read chat when I'm not using it?" With a bot account, it's clear: "the bot reads your chat when you're using the overlay."

**Why we considered this:**
- Initial intuition: "Each streamer should authorize their own data access"
- Reality: Twitch's bot model is designed exactly for this use case (third-party apps reading chat)

### 2. Multiple bot accounts (separate bot per streamer or shard)

**Description:** Create multiple bot accounts (e.g., `chatpulse_bot_1`, `chatpulse_bot_2`) and distribute streamers across them.

**Rejected because:**
- **Token management overhead:** Now we have N bot tokens to manage, refresh, and monitor (one per bot account).
- **Rate limits distributed:** Twitch's rate limits are per user. Splitting across bots doesn't help unless we're hitting per-user limits (we're not - we hit global app limits first).
- **Doesn't simplify anything:** We still need the same EventSub subscription logic. We just add complexity by tracking "which bot is assigned to which streamer?"
- **Operational burden:** Bot account creation, token refresh, monitoring - all multiplied by N.

**When this would make sense:**
- If Twitch had per-user rate limits that we were consistently hitting (not currently the case)
- If we wanted to isolate failures (one bot fails, only some streamers affected) - but token refresh failures are rare

### 3. Twitch IRC fallback (use legacy IRC instead of EventSub)

**Description:** Connect to Twitch's IRC servers (chat.twitch.tv) using the IRC protocol instead of EventSub. Join each streamer's channel as the bot account.

**Rejected because:**
- **Deprecated transport:** Twitch explicitly recommends EventSub for new integrations. IRC is in maintenance mode and may be removed in the future.
- **Doesn't support bot account model properly:** While you can connect as a bot via IRC, Twitch's documentation pushes EventSub + bot scopes for modern apps.
- **Fewer guarantees:** IRC is best-effort. Messages can be dropped during high traffic (Twitch "bursts" messages). EventSub has better reliability.
- **No official Go SDK:** Would need a third-party IRC library or write our own parser. EventSub has Kappopher.

## Consequences

### ✅ Positive

- **Streamers grant minimal permissions:** Only `channel:bot` scope (very specific: "let this bot into my channel"). Much clearer than "read all chat messages."
- **Single token to manage:** The bot's OAuth token is refreshed centrally. If it expires, we refresh once and all streamers are fixed.
- **Simpler EventSub subscription logic:** All subscriptions use the same `user_id` (the bot). Only `broadcaster_user_id` varies per streamer.
- **Clear separation of concerns:** Bot account = read chat. Streamer account = configure overlay. No overlap in permissions.
- **Standard Twitch pattern:** This is exactly how most Twitch bots work (Nightbot, StreamElements, etc.). Familiar to streamers.

### ❌ Negative

- **Single rate limit pool for all streamers:** Twitch's rate limits are applied per user. All EventSub subscriptions from the bot share the same limit (5000 requests per minute for most endpoints). If we hit the limit, all streamers are affected.
- **Bot account is single point of failure:** If the bot's token expires and refresh fails (e.g., bot account password changed, 2FA enabled), all chat reading stops. No per-streamer fallback.
- **Requires bot account setup:** During initial deployment, the operator must manually create a Twitch account, authorize it, and configure `BOT_USER_ID`. Can't skip this step.
- **Bot must be granted access to every channel:** When a streamer signs up, they must explicitly grant `channel:bot` scope. If they revoke this later, we can't read their chat (no fallback mechanism).

### 🔄 Trade-offs

- **Chose simplicity over rate limit isolation:** We accept sharing a single rate limit pool for the benefit of managing one token instead of N. If rate limits become an issue, we can add multiple bots later (this decision doesn't preclude that).
- **Accept single point of failure for simpler auth flow:** The bot token failing is a catastrophic failure, but it's also extremely rare (only happens if bot account is manually changed). We prioritize simpler onboarding over distributed failure modes.
- **Prioritize minimal streamer permissions over distributed rate limits:** Streamers trust us more when we ask for `channel:bot` (narrow, specific) instead of `user:read:chat` (broad, scary). We accept rate limit consolidation for this trust benefit.

## Related Decisions

- **ADR-003: EventSub webhooks + conduits** - Consequence: the bot account receives all chat messages via webhook POST. The `user_id` in EventSub subscriptions is the bot's user ID.
- **ADR-008: OAuth scope minimization** (future) - Philosophy: request the narrowest scopes possible. Bot model allows `channel:bot` instead of `user:read:chat`.

## Implementation Notes

**Bot token refresh:**
The bot's OAuth token is managed separately from streamer tokens. On startup, the app should verify that the bot token is valid (e.g., by calling `GET /users` with the bot's token). If the token is expired or invalid, the app should fail fast with a clear error message.

**Rate limit monitoring:**
Monitor the `RateLimit-Remaining` header on Twitch API responses. If we approach the limit (e.g., <100 requests remaining), log a warning. If we hit the limit, implement backoff and retry.

**Bot account security:**
The bot account's credentials should be treated as secrets. Store the OAuth token securely (same encryption as streamer tokens). Enable 2FA on the bot account to prevent unauthorized access.

## Future Considerations

If we hit per-user rate limits (currently 5000 requests/min), we could:

1. **Add multiple bot accounts:** Implement bot sharding (assign streamers to different bots based on hash of streamer ID)
2. **Request rate limit increase:** Contact Twitch Developer Support to request higher limits for the bot account
3. **Optimize subscription patterns:** Unsubscribe from inactive streamers (no chat activity in 30+ days)

However, these optimizations add complexity. We'll wait for profiling data before implementing.
