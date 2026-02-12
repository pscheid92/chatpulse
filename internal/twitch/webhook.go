package twitch

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const (
	// timestampFreshnessWindow is the maximum age for webhook messages as per Twitch EventSub spec.
	// Messages older than this are rejected to prevent replay attacks with expired messages.
	// Reference: https://dev.twitch.tv/docs/eventsub/handling-webhook-events/
	timestampFreshnessWindow = 10 * time.Minute
)

// WebhookHandler handles Twitch EventSub webhook notifications.
// It uses Kappopher's built-in HMAC verification and processes votes
// through the Engine.
type WebhookHandler struct {
	handler *helix.EventSubWebhookHandler
}

// NewWebhookHandler creates a new WebhookHandler with comprehensive security protection.
//
// Security features (provided by Kappopher):
//   - HMAC-SHA256 signature verification (messageID + timestamp + body)
//   - Message ID deduplication (replay attack protection)
//   - Automatic 403 rejection of invalid/duplicate requests
//
// Additional security (implemented in this handler):
//   - Timestamp freshness validation (10-minute window per Twitch spec)
//   - Defense-in-depth against replay attacks using expired messages
//
// Vote processing pipeline:
//
//	broadcaster lookup → trigger match → debounce check → atomic vote application
//
// See webhook_test.go for comprehensive security test coverage including:
// invalid signatures, missing headers, malformed signatures, tampering attacks, and replay attempts.
func NewWebhookHandler(secret string, engine domain.Engine) *WebhookHandler {
	handler := helix.NewEventSubWebhookHandler(
		helix.WithWebhookSecret(secret),
		helix.WithNotificationHandler(func(msg *helix.EventSubWebhookMessage) {
			// Validate timestamp freshness (defense-in-depth, Kappopher already prevents replays via message ID)
			if !validateTimestampFreshness(msg.MessageTimestamp, time.Now()) {
				age := time.Since(msg.MessageTimestamp)
				slog.Warn("Rejected webhook with stale timestamp",
					"message_id", msg.MessageID,
					"age", age,
					"window", timestampFreshnessWindow)
				return
			}

			if msg.SubscriptionType != helix.EventSubTypeChannelChatMessage {
				return
			}

			event, err := helix.ParseEventSubEvent[helix.ChannelChatMessageEvent](msg)
			if err != nil {
				slog.Error("Failed to parse chat message event", "error", err)
				return
			}

			ctx := context.Background()
			newValue, applied := engine.ProcessVote(ctx, event.BroadcasterUserID, event.ChatterUserID, event.Message.Text)
			if applied {
				slog.Info("Vote processed via webhook", "user", event.ChatterUserID, "value", newValue)
			}
		}),
		helix.WithVerificationHandler(func(msg *helix.EventSubWebhookMessage) bool {
			slog.Info("EventSub webhook verification", "subscription_type", msg.SubscriptionType)
			return true // Accept all subscription verifications
		}),
		helix.WithRevocationHandler(func(msg *helix.EventSubWebhookMessage) {
			slog.Info("EventSub subscription revoked",
				"type", msg.SubscriptionType,
				"reason", helix.GetRevocationReason(msg.Subscription))
		}),
	)

	return &WebhookHandler{handler: handler}
}

// HandleEventSub is an Echo handler that delegates to Kappopher's webhook handler.
func (wh *WebhookHandler) HandleEventSub(c echo.Context) error {
	wh.handler.ServeHTTP(c.Response().Writer, c.Request())
	return nil
}

// HTTPHandler returns the underlying http.Handler for use with standard mux.
func (wh *WebhookHandler) HTTPHandler() http.Handler {
	return wh.handler
}

// validateTimestampFreshness checks if the message timestamp is within the acceptable window.
// Returns true if the timestamp is fresh (within ±10 minutes of current time).
// This provides defense-in-depth against replay attacks using expired messages.
func validateTimestampFreshness(messageTime time.Time, now time.Time) bool {
	age := now.Sub(messageTime)
	if age < 0 {
		age = -age // Handle future timestamps (clock skew)
	}
	return age <= timestampFreshnessWindow
}
