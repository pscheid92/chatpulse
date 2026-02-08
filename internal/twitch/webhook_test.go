package twitch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testWebhookSecret = "test-webhook-secret-1234567890"
	testChatterID     = "chatter-1"
)

func signWebhookRequest(secret, messageID, timestamp, body string) string {
	message := messageID + timestamp + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func makeChatMessageBody(broadcasterUserID, messageText string) string {
	event := map[string]any{
		"broadcaster_user_id":    broadcasterUserID,
		"broadcaster_user_login": "streamer",
		"broadcaster_user_name":  "Streamer",
		"chatter_user_id":        testChatterID,
		"chatter_user_login":     "chatter",
		"chatter_user_name":      "Chatter",
		"message_id":             "msg-123",
		"message": map[string]any{
			"text":      messageText,
			"fragments": []any{},
		},
		"message_type": "text",
		"color":        "",
		"badges":       []any{},
	}

	payload := map[string]any{
		"subscription": map[string]any{
			"id":      "sub-123",
			"type":    helix.EventSubTypeChannelChatMessage,
			"version": "1",
			"status":  "enabled",
			"condition": map[string]string{
				"broadcaster_user_id": broadcasterUserID,
				"user_id":             "bot-user",
			},
			"transport": map[string]string{
				"method":     "webhook",
				"callback":   "https://example.com/webhooks/eventsub",
				"created_at": time.Now().Format(time.RFC3339),
			},
			"created_at": time.Now().Format(time.RFC3339),
		},
		"event": event,
	}

	b, _ := json.Marshal(payload)
	return string(b)
}

func makeSignedNotification(secret, body string) *http.Request {
	messageID := "test-msg-id-" + fmt.Sprintf("%d", time.Now().UnixNano())
	timestamp := time.Now().Format(time.RFC3339)
	signature := signWebhookRequest(secret, messageID, timestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)
	req.Header.Set(helix.EventSubHeaderSubscriptionVersion, "1")
	return req
}

func setupWebhookTest(t *testing.T) (*WebhookHandler, *sentiment.InMemoryStore, string) {
	t.Helper()

	clock := clockwork.NewFakeClock()
	store := sentiment.NewInMemoryStore(clock)

	broadcasterID := "broadcaster-123"
	config := models.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	sessionUUID := mustNewUUID(t)
	err := store.ActivateSession(context.Background(), sessionUUID, broadcasterID, config)
	require.NoError(t, err)

	handler := NewWebhookHandler(testWebhookSecret, store)
	return handler, store, broadcasterID
}

func mustNewUUID(t *testing.T) (id [16]byte) {
	t.Helper()
	// Create a deterministic UUID for testing
	copy(id[:], "test-session-uuid")
	return id
}

func TestWebhook_MatchingTrigger(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes I agree")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)

	sessionUUID := mustNewUUID(t)
	value, exists, err := store.GetSessionValue(context.Background(), sessionUUID)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 10.0, value)
}

func TestWebhook_NoTriggerMatch(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "hello world")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)

	sessionUUID := mustNewUUID(t)
	value, exists, err := store.GetSessionValue(context.Background(), sessionUUID)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 0.0, value)
}

func TestWebhook_DebouncedVote(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	// First vote should apply
	body1 := makeChatMessageBody(broadcasterID, "yes")
	req1 := makeSignedNotification(testWebhookSecret, body1)
	rec1 := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec1, req1)
	assert.Equal(t, 204, rec1.Code)

	// Same chatter, second vote should be debounced
	body2 := makeChatMessageBody(broadcasterID, "yes again")
	req2 := makeSignedNotification(testWebhookSecret, body2)
	rec2 := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec2, req2)
	assert.Equal(t, 204, rec2.Code)

	sessionUUID := mustNewUUID(t)
	value, _, err := store.GetSessionValue(context.Background(), sessionUUID)
	require.NoError(t, err)
	assert.Equal(t, 10.0, value, "debounced vote should not apply twice")
}

func TestWebhook_InvalidSignature(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification("wrong-secret-value-here!!!!!!!", body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 403, rec.Code)
}

func TestWebhook_NonChatSubscriptionType(t *testing.T) {
	handler, store, _ := setupWebhookTest(t)

	// Build a non-chat event payload
	payload := map[string]any{
		"subscription": map[string]any{
			"id":      "sub-456",
			"type":    "channel.follow",
			"version": "2",
			"status":  "enabled",
			"condition": map[string]string{
				"broadcaster_user_id": "broadcaster-123",
			},
			"transport": map[string]string{
				"method":   "webhook",
				"callback": "https://example.com/webhooks/eventsub",
			},
			"created_at": time.Now().Format(time.RFC3339),
		},
		"event": map[string]any{
			"user_id":             "user-789",
			"broadcaster_user_id": "broadcaster-123",
		},
	}
	b, _ := json.Marshal(payload)
	body := string(b)

	messageID := "test-nonch"
	timestamp := time.Now().Format(time.RFC3339)
	signature := signWebhookRequest(testWebhookSecret, messageID, timestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, "channel.follow")
	req.Header.Set(helix.EventSubHeaderSubscriptionVersion, "2")

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)

	// Store value should remain 0
	sessionUUID := mustNewUUID(t)
	value, exists, err := store.GetSessionValue(context.Background(), sessionUUID)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 0.0, value)
}
