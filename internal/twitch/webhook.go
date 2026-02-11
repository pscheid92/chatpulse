package twitch

import (
	"context"
	"log"
	"net/http"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// WebhookHandler handles Twitch EventSub webhook notifications.
// It uses Kappopher's built-in HMAC verification and processes votes
// through the Engine.
type WebhookHandler struct {
	handler *helix.EventSubWebhookHandler
}

// NewWebhookHandler creates a new WebhookHandler with HMAC signature verification.
// Votes are processed through the Engine:
// broadcaster lookup → trigger match → debounce check → atomic vote application.
func NewWebhookHandler(secret string, engine domain.Engine) *WebhookHandler {
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
			newValue, applied := engine.ProcessVote(ctx, event.BroadcasterUserID, event.ChatterUserID, event.Message.Text)
			if applied {
				log.Printf("Vote processed via webhook: user=%s, new value=%.2f", event.ChatterUserID, newValue)
			}
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
