package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/pscheid92/chatpulse/internal/domain"
)

const streamKeyTTL = 10 * time.Minute

type SentimentStore struct {
	rdb *goredis.Client
}

func NewSentimentStore(rdb *goredis.Client) *SentimentStore {
	return &SentimentStore{rdb: rdb}
}

func (s *SentimentStore) RecordVote(ctx context.Context, broadcasterID string, target domain.VoteTarget, windowSeconds int) (*domain.WindowSnapshot, error) {
	sk := votesKey(broadcasterID)
	windowMs := int64(windowSeconds) * 1000
	cutoff := time.Now().UnixMilli() - windowMs
	minID := strconv.FormatInt(cutoff, 10)

	var voteValue string
	switch target {
	case domain.VoteTargetPositive:
		voteValue = "+1"
	case domain.VoteTargetNegative:
		voteValue = "-1"
	default:
		voteValue = "+1"
	}

	// Pipeline: XADD, XTRIM, XRANGE, EXPIRE
	pipe := s.rdb.TxPipeline()
	pipe.XAdd(ctx, &goredis.XAddArgs{
		Stream: sk,
		Values: map[string]any{"vote": voteValue},
	})
	pipe.XTrimMinID(ctx, sk, minID)
	xrangeCmd := pipe.XRange(ctx, sk, minID, "+")
	pipe.Expire(ctx, sk, streamKeyTTL)

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("record vote pipeline failed: %w", err)
	}

	messages, err := xrangeCmd.Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("xrange result failed: %w", err)
	}

	return computeSnapshot(messages), nil
}

func (s *SentimentStore) GetSnapshot(ctx context.Context, broadcasterID string, windowSeconds int) (*domain.WindowSnapshot, error) {
	sk := votesKey(broadcasterID)
	windowMs := int64(windowSeconds) * 1000
	cutoff := time.Now().UnixMilli() - windowMs
	minID := strconv.FormatInt(cutoff, 10)

	// Pipeline: XTRIM, XRANGE
	pipe := s.rdb.TxPipeline()
	pipe.XTrimMinID(ctx, sk, minID)
	xrangeCmd := pipe.XRange(ctx, sk, minID, "+")

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("get snapshot pipeline failed: %w", err)
	}

	messages, err := xrangeCmd.Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("xrange result failed: %w", err)
	}

	return computeSnapshot(messages), nil
}

func (s *SentimentStore) ResetSentiment(ctx context.Context, broadcasterID string) error {
	sk := votesKey(broadcasterID)
	if err := s.rdb.Del(ctx, sk).Err(); err != nil {
		return fmt.Errorf("failed to reset sentiment: %w", err)
	}
	return nil
}

func votesKey(broadcasterID string) string {
	return "votes:" + broadcasterID
}

func computeSnapshot(messages []goredis.XMessage) *domain.WindowSnapshot {
	var forCount, againstCount int
	for _, msg := range messages {
		vote, ok := msg.Values["vote"].(string)
		if !ok {
			continue
		}
		switch vote {
		case "+1":
			forCount++
		case "-1":
			againstCount++
		}
	}

	total := forCount + againstCount
	if total == 0 {
		return &domain.WindowSnapshot{}
	}

	return &domain.WindowSnapshot{
		ForRatio:     float64(forCount) / float64(total),
		AgainstRatio: float64(againstCount) / float64(total),
		TotalVotes:   total,
	}
}
