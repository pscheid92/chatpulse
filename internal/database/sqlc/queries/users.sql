-- name: GetUserByID :one
SELECT id, overlay_uuid, twitch_user_id, twitch_username,
       access_token, refresh_token, token_expiry, created_at, updated_at
FROM users WHERE id = $1;

-- name: GetUserByOverlayUUID :one
SELECT id, overlay_uuid, twitch_user_id, twitch_username,
       access_token, refresh_token, token_expiry, created_at, updated_at
FROM users WHERE overlay_uuid = $1;

-- name: UpsertUser :one
INSERT INTO users (twitch_user_id, twitch_username, access_token, refresh_token, token_expiry, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
ON CONFLICT (twitch_user_id) DO UPDATE SET
    twitch_username = EXCLUDED.twitch_username,
    access_token = EXCLUDED.access_token,
    refresh_token = EXCLUDED.refresh_token,
    token_expiry = EXCLUDED.token_expiry,
    updated_at = NOW()
RETURNING id, overlay_uuid, twitch_user_id, twitch_username,
          access_token, refresh_token, token_expiry, created_at, updated_at;

-- name: InsertDefaultConfig :exec
INSERT INTO configs (user_id, created_at, updated_at)
VALUES ($1, NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING;

-- name: UpdateTokens :execresult
UPDATE users
SET access_token = $1, refresh_token = $2, token_expiry = $3, updated_at = NOW()
WHERE id = $4;

-- name: RotateOverlayUUID :one
UPDATE users
SET overlay_uuid = gen_random_uuid(), updated_at = NOW()
WHERE id = $1
RETURNING overlay_uuid;
