package sentiment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

const voteDelta = 10.0

type Engine struct {
	configSource domain.ConfigSource
	sentiment    domain.SentimentStore
	debounce     domain.Debouncer
	clock        clockwork.Clock
	configCache  *ConfigCache
}

func NewEngine(configSource domain.ConfigSource, sentiment domain.SentimentStore, debounce domain.Debouncer, clock clockwork.Clock, configCache *ConfigCache) *Engine {
	return &Engine{
		configSource: configSource,
		sentiment:    sentiment,
		debounce:     debounce,
		clock:        clock,
		configCache:  configCache,
	}
}

func (e *Engine) GetBroadcastData(ctx context.Context, broadcasterID string) (*domain.BroadcastData, error) {
	config, err := e.getConfig(ctx, broadcasterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get config: %w", err)
	}

	value, lastUpdateMs, err := e.sentiment.GetRawSentiment(ctx, broadcasterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get raw sentiment: %w", err)
	}

	data := domain.BroadcastData{
		Value:         value,
		DecaySpeed:    config.DecaySpeed,
		UnixTimestamp: lastUpdateMs,
	}
	return &data, nil
}

func (e *Engine) ProcessVote(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (float64, domain.VoteResult, error) {
	// Track vote processing duration
	start := e.clock.Now()
	defer func() {
		duration := e.clock.Since(start).Seconds()
		metrics.VoteProcessingDuration.Observe(duration)
	}()

	// Get config by broadcaster_id (cache + fallback to session hash)
	config, err := e.getConfig(ctx, broadcasterUserID)
	if errors.Is(err, domain.ErrConfigNotFound) {
		metrics.VoteProcessingTotal.WithLabelValues(domain.VoteNoSession.String()).Inc()
		return 0, domain.VoteNoSession, nil
	}
	if err != nil {
		metrics.VoteProcessingTotal.WithLabelValues(domain.VoteNoSession.String()).Inc()
		return 0, domain.VoteNoSession, fmt.Errorf("config lookup failed: %w", err)
	}

	delta := matchTrigger(messageText, config)
	if delta == 0 {
		metrics.VoteProcessingTotal.WithLabelValues(domain.VoteNoMatch.String()).Inc()
		return 0, domain.VoteNoMatch, nil
	}

	// Track trigger matches by type
	if delta > 0 {
		metrics.VoteTriggerMatches.WithLabelValues("for").Inc()
	} else {
		metrics.VoteTriggerMatches.WithLabelValues("against").Inc()
	}

	// Check per-user debounce (prevents individual user spam)
	allowed, err := e.debounce.CheckDebounce(ctx, broadcasterUserID, chatterUserID)
	if err != nil || !allowed {
		metrics.VoteProcessingTotal.WithLabelValues(domain.VoteDebounced.String()).Inc()
		return 0, domain.VoteDebounced, nil
	}

	nowMs := e.clock.Now().UnixMilli()
	newValue, err := e.sentiment.ApplyVote(ctx, broadcasterUserID, delta, config.DecaySpeed, nowMs)
	if err != nil {
		slog.Error("ApplyVote error", "error", err)
		metrics.VoteProcessingTotal.WithLabelValues(domain.VoteError.String()).Inc()
		return 0, domain.VoteError, fmt.Errorf("apply vote failed: %w", err)
	}

	metrics.VoteProcessingTotal.WithLabelValues(domain.VoteApplied.String()).Inc()
	return newValue, domain.VoteApplied, nil
}

func (e *Engine) ResetSentiment(ctx context.Context, broadcasterID string) error {
	if err := e.sentiment.ResetSentiment(ctx, broadcasterID); err != nil {
		return fmt.Errorf("failed to reset sentiment: %w", err)
	}
	return nil
}

// getConfig retrieves config for a broadcaster: local cache → ConfigSource (Redis → PostgreSQL).
func (e *Engine) getConfig(ctx context.Context, broadcasterID string) (*domain.ConfigSnapshot, error) {
	// Layer 1: local in-memory cache (10s TTL)
	config, hit := e.configCache.Get(broadcasterID)
	if hit {
		metrics.ConfigCacheHits.Inc()
		return config, nil
	}

	// Layer 2+3: ConfigSource handles Redis cache → PostgreSQL fallback
	metrics.ConfigCacheMisses.Inc()

	fetchedConfig, err := e.configSource.GetConfigByBroadcaster(ctx, broadcasterID)
	if err != nil {
		return nil, fmt.Errorf("config lookup failed: %w", err)
	}
	if fetchedConfig == nil {
		return nil, domain.ErrConfigNotFound
	}

	// Populate local cache
	e.configCache.Set(broadcasterID, *fetchedConfig)
	return fetchedConfig, nil
}

func matchTrigger(messageText string, config *domain.ConfigSnapshot) float64 {
	if config == nil {
		return 0
	}

	trimmed := strings.TrimSpace(messageText)

	if config.ForTrigger != "" && strings.EqualFold(trimmed, config.ForTrigger) {
		return voteDelta
	}
	if config.AgainstTrigger != "" && strings.EqualFold(trimmed, config.AgainstTrigger) {
		return -voteDelta
	}
	return 0
}
