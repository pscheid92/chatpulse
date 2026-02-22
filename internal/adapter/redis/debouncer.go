package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const debounceInterval = 1 * time.Second

type Debouncer struct {
	rdb *goredis.Client
}

func NewDebouncer(rdb *goredis.Client) *Debouncer {
	return &Debouncer{rdb: rdb}
}

// IsDebounced returns true if the user is currently debounced (cannot vote),
// false if the user is allowed to vote (and sets the debounce key).
func (d *Debouncer) IsDebounced(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error) {
	dk := debounceKey(broadcasterID, twitchUserID)

	args := goredis.SetArgs{TTL: debounceInterval, Mode: "NX"}
	_, err := d.rdb.SetArgs(ctx, dk, "1", args).Result()
	if errors.Is(err, goredis.Nil) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to set debounce: %w", err)
	}
	return false, nil
}

func debounceKey(broadcasterID string, twitchUserID string) string {
	return "debounce:" + broadcasterID + ":" + twitchUserID
}
