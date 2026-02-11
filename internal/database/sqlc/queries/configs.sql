-- name: GetConfigByUserID :one
SELECT user_id, for_trigger, against_trigger, left_label, right_label,
       decay_speed, created_at, updated_at
FROM configs WHERE user_id = $1;

-- name: UpdateConfig :execresult
UPDATE configs
SET for_trigger = $1, against_trigger = $2, left_label = $3,
    right_label = $4, decay_speed = $5, updated_at = NOW()
WHERE user_id = $6;
