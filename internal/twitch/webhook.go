package twitch

import (
	"context"
	"log"
	"net/http"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/sentiment"
)

// WebhookHandler handles Twitch EventSub webhook notifications.
// It uses Kappopher's built-in HMAC verification and processes votes
// directly through the SessionStateStore, bypassing the Engine actor.
type WebhookHandler struct {
	handler *helix.EventSubWebhookHandler
}

// NewWebhookHandler creates a new WebhookHandler with HMAC signature verification.
// Votes are processed directly through the store for minimal latency:
// broadcaster lookup → trigger match → debounce check → atomic vote application.
func NewWebhookHandler(secret string, store sentiment.SessionStateStore) *WebhookHandler {
	handler := helix.NewEventSubWebhookHandler(
		helix.WithWebhookSecret(secret),
		helix.WithNotificationHandler(func(msg *helix.EventSubWebhookMessage) {
			if msg.SubscriptionType != helix.EventSubTypeChannelChatMessage {
				return
			}

			event, err := helix.ParseEventSubEvent[helix.ChannelChatMessageEvent](msg)
			if err != nil {
				log.Printf("Failed to parse chat message event: %v", err)
				return
			}

			ctx := context.Background()
			broadcasterUserID := event.BroadcasterUserID
			chatterUserID := event.ChatterUserID
			messageText := event.Message.Text

			// Look up session by broadcaster
			sessionUUID, found, err := store.GetSessionByBroadcaster(ctx, broadcasterUserID)
			if err != nil || !found {
				return
			}

			// Get config for trigger matching
			config, err := store.GetSessionConfig(ctx, sessionUUID)
			if err != nil || config == nil {
				return
			}

			// Match trigger
			delta := sentiment.MatchTrigger(messageText, config)
			if delta == 0 {
				return
			}

			// Check debounce
			allowed, err := store.CheckDebounce(ctx, sessionUUID, chatterUserID)
			if err != nil || !allowed {
				return
			}

			// Apply vote atomically (Lua script publishes via Pub/Sub in Redis mode)
			newValue, err := store.ApplyVote(ctx, sessionUUID, delta)
			if err != nil {
				log.Printf("ApplyVote error: %v", err)
				return
			}
			log.Printf("Vote processed via webhook: user=%s, delta=%.0f, new value=%.2f", chatterUserID, delta, newValue)
		}),
		helix.WithVerificationHandler(func(msg *helix.EventSubWebhookMessage) bool {
			log.Printf("EventSub webhook verification for subscription type: %s", msg.SubscriptionType)
			return true // Accept all subscription verifications
		}),
		helix.WithRevocationHandler(func(msg *helix.EventSubWebhookMessage) {
			log.Printf("EventSub subscription revoked: type=%s reason=%s",
				msg.SubscriptionType, helix.GetRevocationReason(msg.Subscription))
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
