package twitch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/correlation"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	handler := correlation.NewHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(slog.New(handler))
	os.Exit(m.Run())
}

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
	Votes  []domain.VoteTarget
	Config domain.OverlayConfig
}

func newTestStore() *testStore {
	return &testStore{
		sessions: make(map[string]*testSession),
	}
}

func (s *testStore) addSession(broadcasterID string, config domain.OverlayConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[broadcasterID] = &testSession{Config: config}
}

// GetConfigByBroadcaster implements domain.ConfigSource.
func (s *testStore) GetConfigByBroadcaster(_ context.Context, broadcasterID string) (domain.OverlayConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return domain.OverlayConfig{}, domain.ErrConfigNotFound
	}
	return sess.Config, nil
}

// RecordVote implements domain.SentimentStore.
func (s *testStore) RecordVote(_ context.Context, broadcasterID string, target domain.VoteTarget, _ int) (*domain.WindowSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return &domain.WindowSnapshot{}, nil
	}
	sess.Votes = append(sess.Votes, target)
	return s.computeSnapshot(sess), nil
}

// GetSnapshot implements domain.SentimentStore.
func (s *testStore) GetSnapshot(_ context.Context, broadcasterID string, _ int) (*domain.WindowSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return &domain.WindowSnapshot{}, nil
	}
	return s.computeSnapshot(sess), nil
}

// ResetSentiment implements domain.SentimentStore.
func (s *testStore) ResetSentiment(_ context.Context, broadcasterID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[broadcasterID]; ok {
		sess.Votes = nil
	}
	return nil
}

func (s *testStore) computeSnapshot(sess *testSession) *domain.WindowSnapshot {
	var forCount, againstCount int
	for _, v := range sess.Votes {
		switch v {
		case domain.VoteTargetPositive:
			forCount++
		case domain.VoteTargetNegative:
			againstCount++
		}
	}
	total := forCount + againstCount
	if total == 0 {
		return &domain.WindowSnapshot{}
	}
	return &domain.WindowSnapshot{
		ForRatio:     float64(forCount) / float64(total),
		AgainstRatio: float64(againstCount) / float64(total),
		TotalVotes:   total,
	}
}

func (s *testStore) getTotalVotes(broadcasterID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[broadcasterID]
	if !ok {
		return 0
	}
	var count int
	for _, v := range sess.Votes {
		if v == domain.VoteTargetPositive || v == domain.VoteTargetNegative {
			count++
		}
	}
	return count
}

// testDebouncer is a simple in-memory debouncer for webhook tests.
type testDebouncer struct {
	mu        sync.Mutex
	debounced map[string]bool
}

func newTestDebouncer() *testDebouncer {
	return &testDebouncer{debounced: make(map[string]bool)}
}

// IsDebounced returns true if the user is debounced (cannot vote), false if allowed.
func (d *testDebouncer) IsDebounced(_ context.Context, broadcasterID string, twitchUserID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := broadcasterID + ":" + twitchUserID
	if d.debounced[key] {
		return true, nil // already voted, is debounced
	}
	d.debounced[key] = true
	return false, nil // first vote, not debounced
}

// viewerPresenceFunc adapts a function to the ViewerPresence interface for tests.
type viewerPresenceFunc func(string) bool

func (f viewerPresenceFunc) HasViewers(broadcasterID string) bool { return f(broadcasterID) }
func (f viewerPresenceFunc) ViewerCount(broadcasterID string) int {
	if f(broadcasterID) {
		return 1
	}
	return 0
}

var alwaysHasViewers = viewerPresenceFunc(func(string) bool { return true })

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
	messageID := "test-msg-id-" + strconv.FormatInt(time.Now().UnixNano(), 10)
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

// contextCapturingOverlay captures the context passed to ProcessMessage for verification.
type contextCapturingOverlay struct {
	capturedCtx context.Context
	mu          sync.Mutex
	called      chan struct{}
}

func newContextCapturingOverlay() *contextCapturingOverlay {
	return &contextCapturingOverlay{called: make(chan struct{}, 1)}
}

func (o *contextCapturingOverlay) ProcessMessage(ctx context.Context, _, _, _ string) (*domain.WindowSnapshot, domain.VoteResult, domain.VoteTarget, error) {
	o.mu.Lock()
	o.capturedCtx = ctx
	o.mu.Unlock()
	o.called <- struct{}{}
	return nil, domain.VoteNoMatch, domain.VoteTargetNone, nil
}

func (o *contextCapturingOverlay) Reset(_ context.Context, _ string) error {
	return nil
}

func (o *contextCapturingOverlay) getContext() context.Context {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.capturedCtx
}

func setupWebhookTest(t *testing.T) (*WebhookHandler, *testStore, string) {
	t.Helper()

	store := newTestStore()

	broadcasterID := "broadcaster-123"
	config := domain.OverlayConfig{
		MemorySeconds:  30,
		ForTrigger:     "yes",
		ForLabel:       "For",
		AgainstTrigger: "no",
		AgainstLabel:   "Against",
		DisplayMode:    domain.DisplayModeCombined,
	}

	store.addSession(broadcasterID, config)

	debouncer := newTestDebouncer()
	ovl := app.NewOverlay(store, store, debouncer)
	handler := NewWebhookHandler(testWebhookSecret, ovl, alwaysHasViewers, nil, nil, nil)
	return handler, store, broadcasterID
}

func TestWebhook_MatchingTrigger(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HandleEventSub(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 1, store.getTotalVotes(broadcasterID))
}

func TestWebhook_NoTriggerMatch(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "hello world")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HandleEventSub(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 0, store.getTotalVotes(broadcasterID))
}

func TestWebhook_DebouncedVote(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	// First vote should apply
	body1 := makeChatMessageBody(broadcasterID, "yes")
	req1 := makeSignedNotification(testWebhookSecret, body1)
	rec1 := httptest.NewRecorder()
	handler.HandleEventSub(rec1, req1)
	assert.Equal(t, 204, rec1.Code)

	// Same chatter, second vote should be debounced
	body2 := makeChatMessageBody(broadcasterID, "yes")
	req2 := makeSignedNotification(testWebhookSecret, body2)
	rec2 := httptest.NewRecorder()
	handler.HandleEventSub(rec2, req2)
	assert.Equal(t, 204, rec2.Code)

	assert.Equal(t, 1, store.getTotalVotes(broadcasterID), "debounced vote should not apply twice")
}

func TestWebhook_InvalidSignature(t *testing.T) {
	handler, _, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification("wrong-secret-value-here!!!!!!!", body)
	rec := httptest.NewRecorder()

	handler.HandleEventSub(rec, req)

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
	handler.HandleEventSub(rec, req)

	assert.Equal(t, 204, rec.Code)

	// Store value should remain 0
	assert.Equal(t, 0, store.getTotalVotes(broadcasterID))
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
	handler.HandleEventSub(rec, req)

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
			handler.HandleEventSub(rec, req)

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
	handler.HandleEventSub(rec, req)

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
	handler.HandleEventSub(rec, req)

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
	handler.HandleEventSub(rec, req)

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
	handler.HandleEventSub(rec1, req)
	assert.Equal(t, 204, rec1.Code)
	assert.Equal(t, 1, store.getTotalVotes(broadcasterID))

	// Replay same request with identical message ID
	// Kappopher should reject this as a replay attack
	rec2 := httptest.NewRecorder()
	handler.HandleEventSub(rec2, req)
	assert.Equal(t, 403, rec2.Code, "Kappopher implements replay protection - duplicate message IDs are rejected")

	// Value should remain 1 (replay was blocked)
	assert.Equal(t, 1, store.getTotalVotes(broadcasterID))
}

// TestWebhook_ProcessMessageContextHasTimeout verifies that ProcessMessage receives a context with a deadline set.
func TestWebhook_ProcessMessageContextHasTimeout(t *testing.T) {
	ovl := newContextCapturingOverlay()
	handler := NewWebhookHandler(testWebhookSecret, ovl, alwaysHasViewers, nil, nil, nil)

	body := makeChatMessageBody("broadcaster-123", "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HandleEventSub(rec, req)

	assert.Equal(t, 204, rec.Code)

	// Wait for the notification handler to be called
	select {
	case <-ovl.called:
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessMessage was not called within timeout")
	}

	ctx := ovl.getContext()
	assert.NotNil(t, ctx, "ProcessMessage should receive a non-nil context")

	deadline, hasDeadline := ctx.Deadline()
	assert.True(t, hasDeadline, "Context passed to ProcessMessage should have a deadline")
	assert.WithinDuration(t, time.Now().Add(webhookProcessingTimeout), deadline, 2*time.Second,
		"Context deadline should be approximately webhookProcessingTimeout from now")

	corrID, hasCorrID := correlation.ID(ctx)
	assert.True(t, hasCorrID, "Context should carry a correlation ID")
	assert.Len(t, corrID, 8, "Correlation ID should be 8 hex chars")
}

// TestWebhook_NoViewersSkipsProcessMessage verifies that votes are skipped when no viewers are watching.
func TestWebhook_NoViewersSkipsProcessMessage(t *testing.T) {
	_, store, broadcasterID := setupWebhookTest(t)

	// Create overlay with the same store
	ovl := app.NewOverlay(store, store, newTestDebouncer())

	// presence always returns false — no viewers connected
	handler := NewWebhookHandler(testWebhookSecret, ovl, viewerPresenceFunc(func(_ string) bool { return false }), nil, nil, nil)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HandleEventSub(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 0, store.getTotalVotes(broadcasterID), "Vote should be skipped when no viewers")
}

// TestWebhook_WithViewersProcessesVote verifies that votes are processed when viewers are watching.
func TestWebhook_WithViewersProcessesVote(t *testing.T) {
	handler, store, broadcasterID := setupWebhookTest(t)

	body := makeChatMessageBody(broadcasterID, "yes")
	req := makeSignedNotification(testWebhookSecret, body)
	rec := httptest.NewRecorder()

	handler.HandleEventSub(rec, req)

	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, 1, store.getTotalVotes(broadcasterID), "Vote should be applied when viewers are present")
}
