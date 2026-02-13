package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const debounceInterval = 1 * time.Second

type Debouncer struct {
	rdb *goredis.Client
}

func NewDebouncer(rdb *goredis.Client) *Debouncer {
	return &Debouncer{rdb: rdb}
}

func (d *Debouncer) CheckDebounce(ctx context.Context, sid uuid.UUID, twitchUserID string) (bool, error) {
	dk := debounceKey(sid, twitchUserID)
	set, err := d.rdb.SetNX(ctx, dk, "1", debounceInterval).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check debounce: %w", err)
	}
	return set, nil
}

func debounceKey(sid uuid.UUID, twitchUserID string) string {
	return "debounce:" + sid.String() + ":" + twitchUserID
}
