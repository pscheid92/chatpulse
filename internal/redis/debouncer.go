package redis

import (
	"context"
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

func (d *Debouncer) CheckDebounce(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error) {
	dk := debounceKey(broadcasterID, twitchUserID)
	set, err := d.rdb.SetNX(ctx, dk, "1", debounceInterval).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check debounce: %w", err)
	}
	return set, nil
}

func debounceKey(broadcasterID string, twitchUserID string) string {
	return "debounce:" + broadcasterID + ":" + twitchUserID
}
