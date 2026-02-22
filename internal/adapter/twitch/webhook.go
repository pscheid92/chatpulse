package twitch

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const webhookProcessingTimeout = 5 * time.Second

// ViewerPresence checks whether a broadcaster has active overlay viewers.
type ViewerPresence interface {
	HasViewers(broadcasterID string) bool
	ViewerCount(broadcasterID string) int
}

type WebhookHandler struct {
	handler       *helix.EventSubWebhookHandler
	overlay       domain.Overlay
	presence      ViewerPresence
	publisher     domain.EventPublisher
	onVoteApplied func(broadcasterID string)
}

func NewWebhookHandler(secret string, overlay domain.Overlay, presence ViewerPresence, publisher domain.EventPublisher, onVoteApplied func(string)) *WebhookHandler {
	wh := &WebhookHandler{
		overlay:       overlay,
		presence:      presence,
		publisher:     publisher,
		onVoteApplied: onVoteApplied,
	}

	wh.handler = helix.NewEventSubWebhookHandler(
		helix.WithWebhookSecret(secret),
		helix.WithNotificationHandler(wh.handleNotification),
		helix.WithVerificationHandler(func(msg *helix.EventSubWebhookMessage) bool {
			slog.Info("EventSub webhook verification", "subscription_type", msg.SubscriptionType)
			return true
		}),
		helix.WithRevocationHandler(func(msg *helix.EventSubWebhookMessage) {
			slog.Info("EventSub subscription revoked", "type", msg.SubscriptionType, "reason", helix.GetRevocationReason(msg.Subscription))
		}),
	)

	return wh
}

func (wh *WebhookHandler) handleNotification(msg *helix.EventSubWebhookMessage) {
	if msg.SubscriptionType != helix.EventSubTypeChannelChatMessage {
		return
	}

	event, err := helix.ParseEventSubEvent[helix.ChannelChatMessageEvent](msg)
	if err != nil {
		slog.Error("Failed to parse chat message event", "error", err)
		return
	}

	if !wh.presence.HasViewers(event.BroadcasterUserID) {
		slog.Debug("Skipping vote: no viewers", "broadcaster", event.BroadcasterUserID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), webhookProcessingTimeout)
	defer cancel()

	snapshot, result, err := wh.overlay.ProcessMessage(ctx, event.BroadcasterUserID, event.ChatterUserID, event.Message.Text)
	if errors.Is(err, context.DeadlineExceeded) {
		slog.Warn("ProcessMessage timed out", "broadcaster", event.BroadcasterUserID, "timeout", webhookProcessingTimeout)
		return
	}
	if err != nil {
		slog.Error("ProcessMessage failed", "broadcaster", event.BroadcasterUserID, "error", err)
		return
	}

	if result != domain.VoteApplied {
		return
	}

	if wh.onVoteApplied != nil {
		wh.onVoteApplied(event.BroadcasterUserID)
	}

	slog.Info("Vote processed via webhook", "user", event.ChatterUserID, "forRatio", snapshot.ForRatio, "againstRatio", snapshot.AgainstRatio, "totalVotes", snapshot.TotalVotes)
	if wh.publisher == nil {
		return
	}

	if pubErr := wh.publisher.PublishSentimentUpdated(ctx, event.BroadcasterUserID, snapshot); pubErr != nil {
		slog.Error("Failed to publish sentiment update", "broadcaster", event.BroadcasterUserID, "error", pubErr)
	}
}

func (wh *WebhookHandler) HandleEventSub(w http.ResponseWriter, r *http.Request) {
	wh.handler.ServeHTTP(w, r)
}
