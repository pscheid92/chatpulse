-- name: CreateEventSubSubscription :exec
INSERT INTO eventsub_subscriptions (user_id, broadcaster_user_id, subscription_id, conduit_id, created_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (user_id) DO UPDATE SET
    broadcaster_user_id = EXCLUDED.broadcaster_user_id,
    subscription_id = EXCLUDED.subscription_id,
    conduit_id = EXCLUDED.conduit_id;

-- name: GetEventSubByUserID :one
SELECT user_id, broadcaster_user_id, subscription_id, conduit_id, created_at
FROM eventsub_subscriptions WHERE user_id = $1;

-- name: DeleteEventSubByUserID :exec
DELETE FROM eventsub_subscriptions WHERE user_id = $1;

-- name: ListEventSubSubscriptions :many
SELECT user_id, broadcaster_user_id, subscription_id, conduit_id, created_at
FROM eventsub_subscriptions;
