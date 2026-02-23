-- name: GetConfigByStreamerID :one
SELECT streamer_id,
       version,
       created_at,
       updated_at,
       for_trigger,
       for_label,
       against_trigger,
       against_label,
       memory_seconds,
       display_mode,
       theme
FROM configs
WHERE streamer_id = $1;

-- name: GetConfigByBroadcasterID :one
SELECT c.streamer_id,
       c.version,
       c.created_at,
       c.updated_at,
       c.for_trigger,
       c.for_label,
       c.against_trigger,
       c.against_label,
       c.memory_seconds,
       c.display_mode,
       c.theme
FROM configs c
JOIN streamers s ON c.streamer_id = s.id
WHERE s.twitch_user_id = $1;

-- name: UpdateConfig :execresult
UPDATE configs
SET for_trigger     = $1,
    for_label       = $2,
    against_trigger = $3,
    against_label   = $4,
    memory_seconds  = $5,
    display_mode    = $6,
    theme           = $7,
    version         = $8,
    updated_at      = NOW()
WHERE streamer_id = $9;
