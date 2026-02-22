package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pscheid92/chatpulse/internal/domain"
)

type Overlay struct {
	configSource domain.ConfigSource
	sentiment    domain.SentimentStore
	debounce     domain.ParticipantDebouncer
}

func NewOverlay(configSource domain.ConfigSource, sentiment domain.SentimentStore, debounce domain.ParticipantDebouncer) *Overlay {
	return &Overlay{
		configSource: configSource,
		sentiment:    sentiment,
		debounce:     debounce,
	}
}

func (o *Overlay) ProcessMessage(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (*domain.WindowSnapshot, domain.VoteResult, domain.VoteTarget, error) {
	config, err := o.getConfig(ctx, broadcasterUserID)
	if errors.Is(err, domain.ErrConfigNotFound) {
		return nil, domain.VoteNoMatch, domain.VoteTargetNone, nil
	}
	if err != nil {
		return nil, domain.VoteNoMatch, domain.VoteTargetNone, fmt.Errorf("config lookup failed: %w", err)
	}

	target := matchTrigger(messageText, config)
	if target == domain.VoteTargetNone {
		return nil, domain.VoteNoMatch, domain.VoteTargetNone, nil
	}

	debounced, err := o.debounce.IsDebounced(ctx, broadcasterUserID, chatterUserID)
	if err != nil {
		// Fail-open: let votes through on debounce errors rather than silently dropping them
		slog.Warn("Debounce check failed, allowing vote", "broadcaster", broadcasterUserID, "chatter", chatterUserID, "error", err)
	}
	if debounced {
		return nil, domain.VoteDebounced, target, nil
	}

	snapshot, err := o.sentiment.RecordVote(ctx, broadcasterUserID, target, config.MemorySeconds)
	if err != nil {
		slog.Error("RecordVote error", "error", err)
		return nil, domain.VoteNoMatch, target, fmt.Errorf("record vote failed: %w", err)
	}

	return snapshot, domain.VoteApplied, target, nil
}

func (o *Overlay) Reset(ctx context.Context, broadcasterID string) error {
	if err := o.sentiment.ResetSentiment(ctx, broadcasterID); err != nil {
		return fmt.Errorf("failed to reset sentiment: %w", err)
	}
	return nil
}

func (o *Overlay) getConfig(ctx context.Context, broadcasterID string) (domain.OverlayConfig, error) {
	config, err := o.configSource.GetConfigByBroadcaster(ctx, broadcasterID)
	if err != nil {
		return domain.OverlayConfig{}, fmt.Errorf("config lookup failed: %w", err)
	}
	return config, nil
}

func matchTrigger(messageText string, config domain.OverlayConfig) domain.VoteTarget {
	trimmed := strings.TrimSpace(messageText)

	if config.ForTrigger != "" && strings.EqualFold(trimmed, config.ForTrigger) {
		return domain.VoteTargetPositive
	}
	if config.AgainstTrigger != "" && strings.EqualFold(trimmed, config.AgainstTrigger) {
		return domain.VoteTargetNegative
	}
	return domain.VoteTargetNone
}
