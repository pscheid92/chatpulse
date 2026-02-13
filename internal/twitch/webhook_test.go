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
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/stretchr/testify/assert"
)

const (
	testWebhookSecret = "test-webhook-secret-1234567890"
	testChatterID     = "chatter-1"
)

// testStore is a minimal in-memory store for webhook tests.
// Implements domain.ConfigSource and domain.SentimentStore.
type testStore struct {
	mu       sync.Mutex
	sessions map[string]*testSession // keyed by broadcasterID
}

type testSession struct {
	Value  float64
	Config domain.ConfigSnapshot
}

func newTestStore() *testStore {
	return &testStore{
		sessions: make(map[string]*testSession),
	}
}

func (s *testStore) addSession(broadcasterID string, config domain.ConfigSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[broadcasterID] = &testSession{Value: 0, Config: config}
}

// GetConfigByBroadcaster implements domain.ConfigSource.
func (s *testStore) GetConfigByBroadcaster(_ context.Context, broadcasterID string) (*domain.ConfigSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return nil, nil
	}
	cfg := sess.Config
	return &cfg, nil
}

// ApplyVote implements domain.SentimentStore.
func (s *testStore) ApplyVote(_ context.Context, broadcasterID string, delta, _ float64, _ int64) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return 0, nil
	}
	sess.Value = clamp(sess.Value+delta, -100, 100)
	return sess.Value, nil
}

func (s *testStore) getValue(broadcasterID string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return 0
	}
	return sess.Value
}

// Stub methods to satisfy domain.SentimentStore.
func (s *testStore) GetSentiment(_ context.Context, _ string, _ float64, _ int64) (float64, error) {
	return 0, nil
}
func (s *testStore) GetRawSentiment(_ context.Context, _ string) (float64, int64, error) {
	return 0, 0, nil
}
func (s *testStore) ResetSentiment(_ context.Context, _ string) error { return nil }

// testDebouncer is a simple in-memory debouncer for webhook tests.
type testDebouncer struct {
	mu        sync.Mutex
	debounced map[string]bool
}

func newTestDebouncer() *testDebouncer {
	return &testDebouncer{debounced: make(map[string]bool)}
}

func (d *testDebouncer) CheckDebounce(_ context.Context, broadcasterID string, twitchUserID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := broadcasterID + ":" + twitchUserID
	if d.debounced[key] {
		return false, nil
	}
	d.debounced[key] = true
	return true, nil
}

// alwaysAllowRateLimiter always allows votes (for webhook tests).
type alwaysAllowRateLimiter struct{}

func (a *alwaysAllowRateLimiter) CheckVoteRateLimit(_ context.Context, _ string) (bool, error) {
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

// contextCapturingEngine captures the context passed to ProcessVote for verification.
type contextCapturingEngine struct {
	capturedCtx context.Context
	mu          sync.Mutex
	called      chan struct{}
}

func newContextCapturingEngine() *contextCapturingEngine {
	return &contextCapturingEngine{called: make(chan struct{}, 1)}
}

func (e *contextCapturingEngine) ProcessVote(ctx context.Context, _, _, _ string) (float64, domain.VoteResult, error) {
	e.mu.Lock()
	e.capturedCtx = ctx
	e.mu.Unlock()
	e.called <- struct{}{}
	return 0, domain.VoteNoMatch, nil
}

func (e *contextCapturingEngine) GetBroadcastData(_ context.Context, _ string) (*domain.BroadcastData, error) {
	return nil, nil
}

func (e *contextCapturingEngine) ResetSentiment(_ context.Context, _ string) error {
	return nil
}

func (e *contextCapturingEngine) getContext() context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.capturedCtx
}

func setupWebhookTest(t *testing.T) (*WebhookHandler, *testStore, string) {
	t.Helper()

	store := newTestStore()

	broadcasterID := "broadcaster-123"
	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	store.addSession(broadcasterID, config)

	debouncer := newTestDebouncer()
	rateLimiter := &alwaysAllowRateLimiter{}
	clock := clockwork.NewRealClock()
	cache := sentiment.NewConfigCache(10*time.Second, clock)
	engine := sentiment.NewEngine(store, store, debouncer, rateLimiter, clock, cache)
	handler := NewWebhookHandler(testWebhookSecret, engine, nil)
	return handler, store, broadcasterID
}

func TestWebhook_MatchingTrigger(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 10.0, store.getValue(broadcasterID))
}

func TestWebhook_NoTriggerMatch(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "hello world")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 0.0, store.getValue(broadcasterID))
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

	assert.Equal(t, 10.0, store.getValue(broadcasterID), "debounced vote should not apply twice")
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
	handler, store, broadcasterID := setupWebhookTest(t)

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
	assert.Equal(t, 0.0, store.getValue(broadcasterID))
}

// --- HMAC Security Edge Case Tests ---

// TestWebhook_MissingSignatureHeader verifies request is rejected when signature header is missing
func TestWebhook_MissingSignatureHeader(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	messageID := "test-msg-no-sig"
	timestamp := time.Now().Format(time.RFC3339)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	// Intentionally omit signature header
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 403, rec.Code, "Missing signature should be rejected")
}

// TestWebhook_MalformedSignature verifies various malformed signature formats are rejected
func TestWebhook_MalformedSignature(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	testCases := []struct {
		name      string
		signature string
	}{
		{"no prefix", "abcdef1234567890"},
		{"wrong prefix", "md5=abcdef1234567890"},
		{"empty", ""},
		{"only prefix", "sha256="},
		{"invalid hex", "sha256=gggggg"},
		{"truncated", "sha256=abc"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body := makeChatMessageBody(broadcasterID, "yes")
			messageID := "test-msg-" + tc.name
			timestamp := time.Now().Format(time.RFC3339)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(helix.EventSubHeaderMessageID, messageID)
			req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
			req.Header.Set(helix.EventSubHeaderMessageSignature, tc.signature)
			req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
			req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)

			rec := httptest.NewRecorder()
			handler.HTTPHandler().ServeHTTP(rec, req)

			assert.Equal(t, 403, rec.Code, "Malformed signature should be rejected: %s", tc.name)
		})
	}
}

// TestWebhook_TamperedBody verifies tampering with body invalidates signature
func TestWebhook_TamperedBody(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	originalBody := makeChatMessageBody(broadcasterID, "yes")
	tamperedBody := makeChatMessageBody(broadcasterID, "no")

	// Sign the original body but send tampered body
	messageID := "test-msg-tamper"
	timestamp := time.Now().Format(time.RFC3339)
	signature := signWebhookRequest(testWebhookSecret, messageID, timestamp, originalBody)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(tamperedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 403, rec.Code, "Tampered body should invalidate signature")
}

// TestWebhook_TamperedMessageID verifies tampering with message ID invalidates signature
func TestWebhook_TamperedMessageID(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	originalMessageID := "test-msg-original"
	tamperedMessageID := "test-msg-tampered"
	timestamp := time.Now().Format(time.RFC3339)

	// Sign with original message ID
	signature := signWebhookRequest(testWebhookSecret, originalMessageID, timestamp, body)

	// Send with tampered message ID
	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, tamperedMessageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 403, rec.Code, "Tampered message ID should invalidate signature")
}

// TestWebhook_TamperedTimestamp verifies tampering with timestamp invalidates signature
func TestWebhook_TamperedTimestamp(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	messageID := "test-msg-time"
	originalTimestamp := time.Now().Format(time.RFC3339)
	tamperedTimestamp := time.Now().Add(time.Hour).Format(time.RFC3339)

	// Sign with original timestamp
	signature := signWebhookRequest(testWebhookSecret, messageID, originalTimestamp, body)

	// Send with tampered timestamp
	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, tamperedTimestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 403, rec.Code, "Tampered timestamp should invalidate signature")
}

// TestWebhook_ReplayAttack verifies Kappopher implements replay protection
// by rejecting duplicate message IDs
func TestWebhook_ReplayAttack(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)

	// First request should succeed
	rec1 := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec1, req)
	assert.Equal(t, 204, rec1.Code)
	assert.Equal(t, 10.0, store.getValue(broadcasterID))

	// Replay same request with identical message ID
	// Kappopher should reject this as a replay attack
	rec2 := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec2, req)
	assert.Equal(t, 403, rec2.Code, "Kappopher implements replay protection - duplicate message IDs are rejected")

	// Value should remain 10.0 (replay was blocked)
	assert.Equal(t, 10.0, store.getValue(broadcasterID))
}

// --- Timestamp Freshness Tests ---

// TestValidateTimestampFreshness_FreshMessage verifies recent timestamps are accepted
func TestValidateTimestampFreshness_FreshMessage(t *testing.T) {
	now := time.Now()

	testCases := []struct {
		name        string
		messageTime time.Time
		expected    bool
	}{
		{"exact time", now, true},
		{"1 second old", now.Add(-1 * time.Second), true},
		{"5 minutes old", now.Add(-5 * time.Minute), true},
		{"9 minutes old", now.Add(-9 * time.Minute), true},
		{"exactly 10 minutes old", now.Add(-10 * time.Minute), true},
		{"11 minutes old", now.Add(-11 * time.Minute), false},
		{"1 hour old", now.Add(-1 * time.Hour), false},
		{"1 second in future (clock skew)", now.Add(1 * time.Second), true},
		{"5 minutes in future", now.Add(5 * time.Minute), true},
		{"11 minutes in future", now.Add(11 * time.Minute), false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := validateTimestampFreshness(tc.messageTime, now)
			assert.Equal(t, tc.expected, result, "timestamp %v should be %v", tc.messageTime, tc.expected)
		})
	}
}

// TestWebhook_StaleTimestamp verifies stale messages are rejected
func TestWebhook_StaleTimestamp(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")

	// Create request with 15-minute-old timestamp
	staleTime := time.Now().Add(-15 * time.Minute)
	messageID := "test-msg-stale"
	timestamp := staleTime.Format(time.RFC3339)
	signature := signWebhookRequest(testWebhookSecret, messageID, timestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)
	req.Header.Set(helix.EventSubHeaderSubscriptionVersion, "1")

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	// Kappopher rejects stale timestamps (>10 minutes old) with 400 status
	assert.Equal(t, 400, rec.Code, "Kappopher rejects stale timestamps")
	assert.Equal(t, 0.0, store.getValue(broadcasterID), "Stale timestamp prevents vote application")
}

// TestWebhook_FutureTimestamp verifies future timestamps within clock skew window are accepted
func TestWebhook_FutureTimestamp(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")

	// Create request with 5-minute-future timestamp (within 10 minute window)
	futureTime := time.Now().Add(5 * time.Minute)
	messageID := "test-msg-future"
	timestamp := futureTime.Format(time.RFC3339)
	signature := signWebhookRequest(testWebhookSecret, messageID, timestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, timestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)
	req.Header.Set(helix.EventSubHeaderSubscriptionVersion, "1")

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	// Kappopher rejects future timestamps (even within 10-minute window)
	assert.Equal(t, 400, rec.Code, "Kappopher rejects future timestamps")
	assert.Equal(t, 0.0, store.getValue(broadcasterID), "Future timestamp prevents vote application")
}

// --- Timestamp Freshness Validation Tests ---

func TestValidateTimestampFreshness_WithinWindow(t *testing.T) {
	now := time.Now()

	testCases := []struct {
		name      string
		timestamp time.Time
	}{
		{"exact time", now},
		{"1 minute old", now.Add(-1 * time.Minute)},
		{"5 minutes old", now.Add(-5 * time.Minute)},
		{"9 minutes old", now.Add(-9 * time.Minute)},
		{"exactly 10 minutes old", now.Add(-10 * time.Minute)},
		{"1 minute in future", now.Add(1 * time.Minute)},
		{"5 minutes in future", now.Add(5 * time.Minute)},
		{"exactly 10 minutes in future", now.Add(10 * time.Minute)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := validateTimestampFreshness(tc.timestamp, now)
			assert.True(t, result, "Timestamp should be considered fresh: %s", tc.name)
		})
	}
}

func TestValidateTimestampFreshness_OutsideWindow(t *testing.T) {
	now := time.Now()

	testCases := []struct {
		name      string
		timestamp time.Time
	}{
		{"11 minutes old", now.Add(-11 * time.Minute)},
		{"1 hour old", now.Add(-1 * time.Hour)},
		{"1 day old", now.Add(-24 * time.Hour)},
		{"11 minutes in future", now.Add(11 * time.Minute)},
		{"1 hour in future", now.Add(1 * time.Hour)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := validateTimestampFreshness(tc.timestamp, now)
			assert.False(t, result, "Timestamp should be considered stale: %s", tc.name)
		})
	}
}

func TestWebhook_StaleTimestampRejected(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")

	// Create request with stale timestamp (15 minutes old)
	messageID := "test-msg-stale"
	staleTimestamp := time.Now().Add(-15 * time.Minute).Format(time.RFC3339)
	signature := signWebhookRequest(testWebhookSecret, messageID, staleTimestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, staleTimestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)
	req.Header.Set(helix.EventSubHeaderSubscriptionVersion, "1")

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	// Kappopher rejects stale timestamps (>10 minutes old) with 400 status
	// even though the HMAC signature is valid
	assert.Equal(t, 400, rec.Code, "Kappopher rejects stale timestamp")

	// Vote should NOT be applied due to Kappopher's rejection
	assert.Equal(t, 0.0, store.getValue(broadcasterID), "Stale timestamp prevents vote processing")
}

func TestWebhook_FreshTimestampAccepted(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")

	// Create request with fresh timestamp (5 minutes old, well within window)
	messageID := "test-msg-fresh"
	freshTimestamp := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	signature := signWebhookRequest(testWebhookSecret, messageID, freshTimestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(helix.EventSubHeaderMessageID, messageID)
	req.Header.Set(helix.EventSubHeaderMessageTimestamp, freshTimestamp)
	req.Header.Set(helix.EventSubHeaderMessageSignature, signature)
	req.Header.Set(helix.EventSubHeaderMessageType, helix.EventSubMessageTypeNotification)
	req.Header.Set(helix.EventSubHeaderSubscriptionType, helix.EventSubTypeChannelChatMessage)
	req.Header.Set(helix.EventSubHeaderSubscriptionVersion, "1")

	rec := httptest.NewRecorder()
	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)

	// Vote should be applied since timestamp is fresh
	assert.Equal(t, 10.0, store.getValue(broadcasterID), "Fresh timestamp should allow vote processing")
}

// TestWebhook_ProcessVoteContextHasTimeout verifies that ProcessVote receives a context with a deadline set.
func TestWebhook_ProcessVoteContextHasTimeout(t *testing.T) {
	engine := newContextCapturingEngine()
	handler := NewWebhookHandler(testWebhookSecret, engine, nil)

	body := makeChatMessageBody("broadcaster-123", "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)

	// Wait for the notification handler to be called
	select {
	case <-engine.called:
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessVote was not called within timeout")
	}

	ctx := engine.getContext()
	assert.NotNil(t, ctx, "ProcessVote should receive a non-nil context")

	deadline, hasDeadline := ctx.Deadline()
	assert.True(t, hasDeadline, "Context passed to ProcessVote should have a deadline")
	assert.WithinDuration(t, time.Now().Add(webhookProcessingTimeout), deadline, 2*time.Second,
		"Context deadline should be approximately webhookProcessingTimeout from now")
}

// TestWebhook_NoViewersSkipsProcessVote verifies that votes are skipped when no viewers are watching.
func TestWebhook_NoViewersSkipsProcessVote(t *testing.T) {
	_, store, broadcasterID := setupWebhookTest(t)

	// Create engine with the same store
	clock := clockwork.NewRealClock()
	cache := sentiment.NewConfigCache(10*time.Second, clock)
	engine := sentiment.NewEngine(store, store, newTestDebouncer(), &alwaysAllowRateLimiter{}, clock, cache)

	// hasViewers always returns false — no viewers connected
	hasViewers := func(_ string) bool { return false }
	handler := NewWebhookHandler(testWebhookSecret, engine, hasViewers)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 0.0, store.getValue(broadcasterID), "Vote should be skipped when no viewers")
}

// TestWebhook_WithViewersProcessesVote verifies that votes are processed when viewers are watching.
func TestWebhook_WithViewersProcessesVote(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	// Default setupWebhookTest passes nil hasViewers (no skip check).
	// Verify votes still work normally.
	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HTTPHandler().ServeHTTP(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 10.0, store.getValue(broadcasterID), "Vote should be applied when hasViewers is nil")
}
