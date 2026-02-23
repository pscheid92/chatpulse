package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/pscheid92/chatpulse/internal/adapter/metrics"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const publishTimeout = 2 * time.Second

type sessionUpdate struct {
	ForRatio     float64 `json:"forRatio"`
	AgainstRatio float64 `json:"againstRatio"`
	TotalVotes   int     `json:"totalVotes"`
	DisplayMode  string  `json:"displayMode"`
	Theme        string  `json:"theme"`
	Status       string  `json:"status"`
}

type Publisher struct {
	node         *centrifuge.Node
	configSource domain.ConfigSource
	wsMetrics    *metrics.WebSocketMetrics
}

func NewPublisher(node *centrifuge.Node, configSource domain.ConfigSource, wsMetrics *metrics.WebSocketMetrics) *Publisher {
	return &Publisher{node: node, configSource: configSource, wsMetrics: wsMetrics}
}

func (p *Publisher) PublishSentiment(ctx context.Context, broadcasterID string, snapshot *domain.WindowSnapshot) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	var displayMode string
	var theme string

	config, err := p.configSource.GetConfigByBroadcaster(ctx, broadcasterID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get config for publish, using defaults", "broadcaster_id", broadcasterID, "error", err)
		displayMode = string(domain.DisplayModeCombined)
		theme = string(domain.ThemeDark)
	} else {
		displayMode = string(config.DisplayMode)
		theme = string(config.Theme)
	}

	update := sessionUpdate{
		ForRatio:     snapshot.ForRatio,
		AgainstRatio: snapshot.AgainstRatio,
		TotalVotes:   snapshot.TotalVotes,
		DisplayMode:  displayMode,
		Theme:        theme,
		Status:       "active",
	}
	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal session update: %w", err)
	}

	channel := "sentiment:" + broadcasterID
	_, err = p.node.Publish(channel, data)
	if err != nil {
		return fmt.Errorf("publish to channel %s: %w", channel, err)
	}

	if p.wsMetrics != nil {
		p.wsMetrics.MessagesPublished.Inc()
	}

	return nil
}
