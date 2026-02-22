-- name: GetStreamerByID :one
SELECT id,
       created_at,
       updated_at,
       overlay_uuid,
       twitch_user_id,
       twitch_username,
       access_token,
       refresh_token,
       token_expiry
FROM streamers
WHERE id = $1;

-- name: GetStreamerByOverlayUUID :one
SELECT id,
       created_at,
       updated_at,
       overlay_uuid,
       twitch_user_id,
       twitch_username,
       access_token,
       refresh_token,
       token_expiry
FROM streamers
WHERE overlay_uuid = $1;

-- name: UpsertStreamer :one
INSERT INTO streamers (
    twitch_user_id,
    twitch_username,
    access_token,
    refresh_token,
    token_expiry,
    created_at,
    updated_at
)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
ON CONFLICT (twitch_user_id) DO UPDATE
    SET twitch_username = EXCLUDED.twitch_username,
        access_token   = EXCLUDED.access_token,
        refresh_token  = EXCLUDED.refresh_token,
        token_expiry   = EXCLUDED.token_expiry,
        updated_at     = NOW()
RETURNING id,
          created_at,
          updated_at,
          overlay_uuid,
          twitch_user_id,
          twitch_username,
          access_token,
          refresh_token,
          token_expiry;

-- name: InsertDefaultConfig :exec
INSERT INTO configs (streamer_id, created_at, updated_at)
VALUES ($1, NOW(), NOW())
ON CONFLICT (streamer_id) DO NOTHING;

-- name: RotateOverlayUUID :one
UPDATE streamers
SET overlay_uuid = uuidv7(),
    updated_at   = NOW()
WHERE id = $1
RETURNING overlay_uuid;
