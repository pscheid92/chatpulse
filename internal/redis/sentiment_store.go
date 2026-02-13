package redis

import (
	"context"
	"fmt"
	"strconv"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// Redis Function names (loaded from chatpulse.lua).
	fnApplyVote    = "apply_vote"
	fnGetSentiment = "get_decayed_value"
)

type SentimentStore struct {
	rdb *goredis.Client
}

func NewSentimentStore(rdb *goredis.Client) *SentimentStore {
	return &SentimentStore{rdb: rdb}
}

func (s *SentimentStore) ApplyVote(ctx context.Context, broadcasterID string, delta, decayRate float64, nowMs int64) (float64, error) {
	sk := sentimentKey(broadcasterID)
	keys := []string{sk}

	deltaArg := strconv.FormatFloat(delta, 'f', -1, 64)
	decayRateArg := strconv.FormatFloat(decayRate, 'f', -1, 64)
	nowMsArg := strconv.FormatInt(nowMs, 10)

	result, err := s.rdb.FCall(ctx, fnApplyVote, keys, deltaArg, decayRateArg, nowMsArg).Text()
	if err != nil {
		return 0, fmt.Errorf("%s function failed: %w", fnApplyVote, err)
	}

	value, err := strconv.ParseFloat(result, 64)
	if err != nil {
		return 0, fmt.Errorf("%s returned invalid float value %q: %w", fnApplyVote, result, err)
	}
	return value, nil
}

func (s *SentimentStore) GetSentiment(ctx context.Context, broadcasterID string, decayRate float64, nowMs int64) (float64, error) {
	sk := sentimentKey(broadcasterID)
	keys := []string{sk}

	decayRateArg := strconv.FormatFloat(decayRate, 'f', -1, 64)
	nowMsArg := strconv.FormatInt(nowMs, 10)

	result, err := s.rdb.FCallRO(ctx, fnGetSentiment, keys, decayRateArg, nowMsArg).Text()
	if err != nil {
		return 0, fmt.Errorf("%s function failed: %w", fnGetSentiment, err)
	}

	value, err := strconv.ParseFloat(result, 64)
	if err != nil {
		return 0, fmt.Errorf("%s returned invalid float value %q: %w", fnGetSentiment, result, err)
	}
	return value, nil
}

func (s *SentimentStore) GetRawSentiment(ctx context.Context, broadcasterID string) (float64, int64, error) {
	sk := sentimentKey(broadcasterID)
	vals, err := s.rdb.HMGet(ctx, sk, "value", "last_update").Result()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get raw sentiment: %w", err)
	}

	var value float64
	var lastUpdate int64

	if vals[0] != nil {
		value, err = strconv.ParseFloat(vals[0].(string), 64)
		if err != nil {
			value = 0 // graceful degradation for corrupt data
		}
	}
	if vals[1] != nil {
		lastUpdate, err = strconv.ParseInt(vals[1].(string), 10, 64)
		if err != nil {
			lastUpdate = 0
		}
	}

	return value, lastUpdate, nil
}

func (s *SentimentStore) ResetSentiment(ctx context.Context, broadcasterID string) error {
	sk := sentimentKey(broadcasterID)
	if err := s.rdb.Del(ctx, sk).Err(); err != nil {
		return fmt.Errorf("failed to reset sentiment: %w", err)
	}
	return nil
}

func sentimentKey(broadcasterID string) string {
	return "sentiment:" + broadcasterID
}
