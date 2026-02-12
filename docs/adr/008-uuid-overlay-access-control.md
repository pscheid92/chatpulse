# ADR-008: UUID-based overlay access control

**Status:** Accepted

**Date:** 2026-02-12

## Context

ChatPulse overlays are embedded in OBS (Open Broadcaster Software) and displayed on live streams. The overlay shows a sentiment bar that updates in real-time based on chat messages. Key constraints:

- **OBS browser sources** load the overlay URL (e.g., `https://chatpulse.com/overlay/abc-123-def`)
- **Viewers watch the stream** and see the overlay, but they don't interact with it directly (it's embedded in the video)
- **CORS requirements:** OBS browser sources may load from `file://` protocol (local HTML), streaming services (Twitch, YouTube), or direct browser navigation. We must allow all origins.
- **Security model:** Viewers should NOT be able to control the sentiment bar (no write access), but they can see it (read-only).
- **Streamer experience:** Copy-paste URL into OBS. Zero configuration. No passwords, no OAuth.

The challenge: How do we control access to overlays without adding complexity to the OBS setup?

## Decision

**Use a separate `overlay_uuid` column as a bearer token in the URL. No authentication required for read access.**

Specifically:

- **Separate UUID for overlay access:** Each user has an `overlay_uuid` column (distinct from their internal `id`). This UUID is used in the overlay URL: `/overlay/{overlay_uuid}`.
- **Bearer token model:** Knowing the UUID grants read access to the overlay. No password, no session cookie, no OAuth.
- **UUID rotation:** Streamers can rotate their overlay UUID via `POST /api/rotate-overlay-uuid` (authenticated route). This invalidates the old URL and generates a new one.
- **Public read access:** Anyone with the UUID can view the overlay (WebSocket endpoint, overlay page). This is intentional - the overlay is embedded in a public stream anyway.
- **CORS policy:** Accept all origins (`Access-Control-Allow-Origin: *`). Required for OBS browser sources which may load from file://, localhost, or streaming service domains.

**URL structure:**
```
https://chatpulse.com/overlay/9e966dab-cee6-4c1e-aa52-7349799e3b62
                             ↑
                             overlay_uuid (128-bit UUID v4)
```

**Authentication separation:**
- `/dashboard` routes → **Twitch OAuth required** (session cookie)
- `/overlay/{uuid}` routes → **No authentication** (public if you know UUID)
- `/api/rotate-overlay-uuid` → **Twitch OAuth required** (only streamer can rotate)

**Threat model:**

| Threat | Protected? | Mitigation |
|--------|-----------|------------|
| Casual guessing | ✅ Yes | 128-bit UUID = 2^128 possible values (astronomically large) |
| URL leakage (stream screenshot, network sniff) | ❌ No | Overlay is public anyway (embedded in stream). Rotation invalidates old URLs. |
| Old URLs after rotation | ✅ Yes | Lookup by old UUID returns 404 (user not found) |
| Sentiment manipulation | ✅ Yes | Overlay is read-only. Sentiment changes require Twitch chat access (separate threat surface). |

**Design principle:** The overlay URL is **convenience security**, not **cryptographic security**. It prevents casual discovery but doesn't protect against determined adversaries. This is acceptable because:

1. The overlay is embedded in a public stream (anyone watching can see it)
2. The overlay is read-only (viewers can't change sentiment)
3. Rotation provides escape hatch (if URL leaks, streamer can rotate)

## Alternatives Considered

### 1. Streamer passwords (password in query string)

**Description:** Require a password in the overlay URL: `/overlay/{uuid}?password=secret123`. OBS browser source includes the password.

**Rejected because:**
- **Same leakage risk as UUID:** Passwords in URLs are just as visible as UUIDs (network sniffing, OBS screenshots, browser history). No security improvement.
- **More complex OBS setup:** Streamers must configure a password and remember to include it in the URL. Copy-paste becomes two-step (URL + password).
- **Harder to rotate:** Rotating requires updating OBS configuration (new password). With UUID rotation, the URL itself changes (no separate password field).
- **Worse UX:** "Why do I need a password for something that's publicly visible in my stream?"

### 2. OAuth for viewers (viewers log in with Twitch to see overlay)

**Description:** Require viewers to authenticate with Twitch before accessing the overlay. Only authorized viewers can see the sentiment bar.

**Rejected because:**
- **Overlay is embedded in stream:** Viewers don't navigate to the overlay URL directly - they watch the stream (which includes the overlay as part of the video). Requiring OAuth makes no sense for this use case.
- **Viewers don't interact with overlay:** The overlay is read-only. There's no reason to authenticate viewers (they can't change anything).
- **Adds complexity for zero gain:** The overlay is public (part of the stream). Authenticating viewers doesn't protect anything - the stream is already public on Twitch/YouTube.

### 3. Signed URLs with expiry (JWT tokens)

**Description:** Generate a signed URL with an expiration timestamp: `/overlay/{uuid}?token=jwt-signed-token`. Token expires after 24 hours, requiring streamer to refresh.

**Rejected because:**
- **More complex implementation:** Need signing key management, JWT encoding/decoding, expiry validation. UUID lookup is much simpler.
- **Rotation already solves expiry use case:** If a URL leaks, the streamer rotates the UUID. Expiry doesn't add meaningful security (the attacker can use the URL during the 24h window).
- **OBS URLs shouldn't expire:** Requiring streamers to update their OBS browser source every 24 hours is annoying. The overlay should "just work" indefinitely.
- **Still leaks in URL:** JWT tokens in query strings have the same leakage risks as UUIDs (network sniffing, logs, screenshots).

### 4. IP whitelisting (only allow access from streamer's IP)

**Description:** Store streamer's IP address and only allow overlay access from that IP.

**Rejected because:**
- **Doesn't match use case:** The overlay is accessed by **OBS** (running on streamer's machine), not by the streamer's browser. And it's embedded in the stream (accessed by viewers' machines, not streamer's).
- **Viewers wouldn't see overlay:** If we whitelist only the streamer's IP, viewers watching the stream wouldn't see the overlay (it's embedded in the video, but the browser source tries to load from viewer's IP when streaming services re-encode).
- **Dynamic IPs:** Many streamers have dynamic IPs (residential internet). Whitelist would break on IP change.

## Consequences

### ✅ Positive

- **Zero-config OBS setup:** Streamer copies the URL from dashboard, pastes into OBS browser source. Done. No passwords, no OAuth, no configuration.
- **Simple rotation:** One API call (`POST /api/rotate-overlay-uuid`) invalidates the old URL and generates a new one. Update OBS with new URL.
- **No CORS issues:** `Access-Control-Allow-Origin: *` allows OBS browser sources to load from any origin (file://, localhost, streaming services). No CORS preflight complexity.
- **Works everywhere:** Local OBS, cloud OBS (StreamYard), direct browser navigation - all work with the same URL.
- **Read-only public access matches reality:** The overlay is public (embedded in stream). URL-based access matches this reality.

### ❌ Negative

- **URL leakage = public access (until rotated):** If the overlay URL appears in a stream screenshot, OBS tutorial, or network packet capture, anyone with that URL can view the overlay. Mitigation: rotation invalidates old URLs.
- **No fine-grained permissions:** Can't grant access to "viewer A" but not "viewer B". It's all-or-nothing (know the UUID = see the overlay). This is acceptable because the overlay is public anyway.
- **Screenshot of OBS config exposes URL:** If a streamer shares a screenshot of their OBS setup (e.g., in a tutorial), the overlay URL is visible. Mitigation: educate streamers to rotate after sharing screenshots.
- **No automatic expiry:** Unlike signed tokens with TTL, overlay URLs don't expire automatically. Must rely on manual rotation. This is intentional (expiry would require constant OBS updates).

### 🔄 Trade-offs

- **Chose simplicity over fine-grained control:** We could implement OAuth or signed tokens, but the complexity doesn't match the threat model. The overlay is public (part of the stream), so simple UUID-based access is sufficient.
- **Accept URL leakage risk:** The overlay is already public (embedded in stream). URL leakage doesn't expose anything that isn't already visible. Rotation provides escape hatch if needed.
- **Prioritize OBS ease-of-use over perfect security:** Zero-config setup (copy-paste URL) is more important than preventing URL leakage. Streamers can rotate if a URL is compromised.

## Security Posture

**What is protected:**
- ✅ Casual discovery (guessing UUIDs is infeasible - 2^128 space)
- ✅ Sentiment manipulation (overlay is read-only, requires Twitch chat access to change)
- ✅ Old URLs after rotation (lookup returns 404)

**What is NOT protected:**
- ❌ URL leakage via screenshots, network sniffing, or sharing
- ❌ Determined adversary who obtains the URL (but they still can't write, only read)

**Threat surface separation:**
- **Overlay access (this ADR):** Read-only, UUID-based
- **Sentiment manipulation:** Requires Twitch chat access (Twitch's authentication, not ours)
- **Config changes:** Requires Twitch OAuth (session cookie, handled separately)

**Rotation workflow:**
1. Streamer suspects URL is compromised (shared in screenshot, etc.)
2. Log into dashboard → click "Rotate Overlay URL"
3. Copy new URL → update OBS browser source
4. Old URL now returns 404

## Related Decisions

- **ADR-009: Token encryption at rest** - Contrast: streamer OAuth tokens are encrypted (sensitive credentials), but overlay UUIDs are intentionally public (bearer tokens).
- **ADR-004: Single bot account** - Context: sentiment manipulation requires Twitch chat access. The bot reads chat, but viewers can't impersonate the bot (Twitch's auth prevents this).

## Implementation Notes

**Database schema:**
```sql
CREATE TABLE users (
    id UUID PRIMARY KEY,
    overlay_uuid UUID NOT NULL UNIQUE,  -- Separate from internal id
    -- ...
);
```

**Route handlers:**
- `GET /overlay/:uuid` - Serve overlay HTML (public access)
- `GET /ws/overlay/:uuid` - WebSocket endpoint (public access)
- `POST /api/rotate-overlay-uuid` - Rotate UUID (requires Twitch OAuth)

**Rotation implementation:**
```go
// database/user_repository.go
func (r *UserRepo) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
    newUUID := uuid.New()
    _, err := r.pool.Exec(ctx, `UPDATE users SET overlay_uuid = $1 WHERE id = $2`, newUUID, userID)
    return newUUID, err
}
```

**CORS configuration:**
```go
// server/server.go
e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
    AllowOrigins: []string{"*"},  // Required for OBS browser sources
}))
```

## Future Considerations

If URL leakage becomes a problem in practice (e.g., many streamers report "someone is using my overlay URL"), we could:

1. **Rate limiting:** Limit how many WebSocket connections can use the same UUID simultaneously (e.g., max 50 clients per session)
2. **Automatic rotation:** Offer optional auto-rotation (e.g., rotate UUID every 30 days) with email notification
3. **Usage analytics:** Show streamers "10 clients connected to your overlay right now" to detect unauthorized access

However, these features add complexity. Current approach (manual rotation on demand) is sufficient for foreseeable needs.
