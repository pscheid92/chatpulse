package models

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID             uuid.UUID `db:"id"`
	OverlayUUID    uuid.UUID `db:"overlay_uuid"`
	TwitchUserID   string    `db:"twitch_user_id"`
	TwitchUsername string    `db:"twitch_username"`
	AccessToken    string    `db:"access_token"`
	RefreshToken   string    `db:"refresh_token"`
	TokenExpiry    time.Time `db:"token_expiry"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

type Config struct {
	UserID         uuid.UUID `db:"user_id"`
	ForTrigger     string    `db:"for_trigger"`
	AgainstTrigger string    `db:"against_trigger"`
	LeftLabel      string    `db:"left_label"`
	RightLabel     string    `db:"right_label"`
	DecaySpeed     float64   `db:"decay_speed"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

type ConfigSnapshot struct {
	ForTrigger     string
	AgainstTrigger string
	LeftLabel      string
	RightLabel     string
	DecaySpeed     float64
}
