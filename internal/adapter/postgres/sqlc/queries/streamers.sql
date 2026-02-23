-- name: GetStreamerByID :one
SELECT id,
       created_at,
       updated_at,
       overlay_uuid,
       twitch_user_id,
       twitch_username
FROM streamers
WHERE id = $1;

-- name: GetStreamerByOverlayUUID :one
SELECT id,
       created_at,
       updated_at,
       overlay_uuid,
       twitch_user_id,
       twitch_username
FROM streamers
WHERE overlay_uuid = $1;

-- name: UpsertStreamer :one
INSERT INTO streamers (
    twitch_user_id,
    twitch_username,
    created_at,
    updated_at
)
VALUES ($1, $2, NOW(), NOW())
ON CONFLICT (twitch_user_id) DO UPDATE
    SET twitch_username = EXCLUDED.twitch_username,
        updated_at     = NOW()
RETURNING id,
          created_at,
          updated_at,
          overlay_uuid,
          twitch_user_id,
          twitch_username;

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
