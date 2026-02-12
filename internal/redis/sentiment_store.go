package redis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
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

func (s *SentimentStore) ApplyVote(ctx context.Context, sid uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error) {
	sk := sessionKey(sid)
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

func (s *SentimentStore) GetSentiment(ctx context.Context, sid uuid.UUID, decayRate float64, nowMs int64) (float64, error) {
	sk := sessionKey(sid)
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

func (s *SentimentStore) ResetSentiment(ctx context.Context, sid uuid.UUID) error {
	sk := sessionKey(sid)
	return s.rdb.HSet(ctx, sk, fieldValue, "0").Err()
}
