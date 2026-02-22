-- name: CreateEventSubSubscription :exec
INSERT INTO eventsub_subscriptions (
    streamer_id,
    subscription_id,
    conduit_id,
    created_at
)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (streamer_id) DO UPDATE
    SET subscription_id     = EXCLUDED.subscription_id,
        conduit_id          = EXCLUDED.conduit_id;

-- name: GetEventSubByStreamerID :one
SELECT e.streamer_id,
       s.twitch_user_id AS broadcaster_user_id,
       e.subscription_id,
       e.conduit_id,
       e.created_at
FROM eventsub_subscriptions e
JOIN streamers s ON e.streamer_id = s.id
WHERE e.streamer_id = $1;

-- name: DeleteEventSubByStreamerID :exec
DELETE
FROM eventsub_subscriptions
WHERE streamer_id = $1;

-- name: DeleteEventSubByConduitID :exec
DELETE
FROM eventsub_subscriptions
WHERE conduit_id = $1;

-- name: ListEventSubSubscriptions :many
SELECT e.streamer_id,
       s.twitch_user_id AS broadcaster_user_id,
       e.subscription_id,
       e.conduit_id,
       e.created_at
FROM eventsub_subscriptions e
JOIN streamers s ON e.streamer_id = s.id;
