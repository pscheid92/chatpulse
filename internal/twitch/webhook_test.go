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
	"sync"
	"testing"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/stretchr/testify/assert"
)

const (
	testWebhookSecret = "test-webhook-secret-1234567890"
	testChatterID     = "chatter-1"
)

// testSessionRepo is a minimal in-memory SessionStore for webhook tests.
type testSessionRepo struct {
	mu                   sync.Mutex
	sessions             map[uuid.UUID]*testSession
	broadcasterToSession map[string]uuid.UUID
}

type testSession struct {
	Value  float64
	Config domain.ConfigSnapshot
}

func newTestSessionStore() *testSessionRepo {
	return &testSessionRepo{
		sessions:             make(map[uuid.UUID]*testSession),
		broadcasterToSession: make(map[string]uuid.UUID),
	}
}

func (s *testSessionRepo) addSession(sessionUUID uuid.UUID, broadcasterID string, config domain.ConfigSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionUUID] = &testSession{Value: 0, Config: config}
	s.broadcasterToSession[broadcasterID] = sessionUUID
}

func (s *testSessionRepo) GetSessionByBroadcaster(_ context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.broadcasterToSession[broadcasterUserID]
	return id, ok, nil
}

func (s *testSessionRepo) GetSessionConfig(_ context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionUUID]
	if !ok {
		return nil, nil
	}
	cfg := sess.Config
	return &cfg, nil
}

func (s *testSessionRepo) ApplyVote(_ context.Context, sessionUUID uuid.UUID, delta, _ float64, _ int64) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionUUID]
	if !ok {
		return 0, nil
	}
	sess.Value = clamp(sess.Value+delta, -100, 100)
	return sess.Value, nil
}

func (s *testSessionRepo) getValue(sessionUUID uuid.UUID) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionUUID]
	if !ok {
		return 0
	}
	return sess.Value
}

// Stub methods to satisfy domain.SessionRepository.
func (s *testSessionRepo) ActivateSession(context.Context, uuid.UUID, string, domain.ConfigSnapshot) error {
	return nil
}
func (s *testSessionRepo) ResumeSession(context.Context, uuid.UUID) error         { return nil }
func (s *testSessionRepo) SessionExists(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (s *testSessionRepo) DeleteSession(context.Context, uuid.UUID) error         { return nil }
func (s *testSessionRepo) MarkDisconnected(context.Context, uuid.UUID) error      { return nil }
func (s *testSessionRepo) UpdateConfig(context.Context, uuid.UUID, domain.ConfigSnapshot) error {
	return nil
}
func (s *testSessionRepo) IncrRefCount(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (s *testSessionRepo) DecrRefCount(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (s *testSessionRepo) ListOrphans(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
	return nil, nil
}

// Stub methods to satisfy domain.SentimentStore.
func (s *testSessionRepo) GetSentiment(context.Context, uuid.UUID, float64, int64) (float64, error) {
	return 0, nil
}
func (s *testSessionRepo) ResetSentiment(context.Context, uuid.UUID) error { return nil }

// testDebouncer is a simple in-memory debouncer for webhook tests.
type testDebouncer struct {
	mu        sync.Mutex
	debounced map[string]bool
}

func newTestDebouncer() *testDebouncer {
	return &testDebouncer{debounced: make(map[string]bool)}
}

func (d *testDebouncer) CheckDebounce(_ context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := sessionUUID.String() + ":" + twitchUserID
	if d.debounced[key] {
		return false, nil
	}
	d.debounced[key] = true
	return true, nil
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

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

// deterministic UUID for tests
var testSessionUUID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func setupWebhookTest(t *testing.T) (*WebhookHandler, *testSessionRepo, string) {
	t.Helper()

	store := newTestSessionStore()

	broadcasterID := "broadcaster-123"
	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	store.addSession(testSessionUUID, broadcasterID, config)

	debouncer := newTestDebouncer()
	engine := sentiment.NewEngine(store, store, debouncer, clockwork.NewRealClock())
	handler := NewWebhookHandler(testWebhookSecret, engine)
	return handler, store, broadcasterID
}

func TestWebhook_MatchingTrigger(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 10.0, store.getValue(testSessionUUID))
}

func TestWebhook_NoTriggerMatch(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "hello world")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 0.0, store.getValue(testSessionUUID))
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
	body2 := makeChatMessageBody(broadcasterID, "yes")
	req2 := makeSignedNotification(testWebhookSecret, body2)
	rec2 := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec2, req2)
	assert.Equal(t, 204, rec2.Code)

	assert.Equal(t, 10.0, store.getValue(testSessionUUID), "debounced vote should not apply twice")
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
	assert.Equal(t, 0.0, store.getValue(testSessionUUID))
}
