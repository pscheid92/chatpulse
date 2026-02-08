package sentiment

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type mockConfigStore struct {
	mu        sync.Mutex
	config    *models.Config
	err       error
	callCount int
}

func (m *mockConfigStore) GetConfig(ctx context.Context, userID uuid.UUID) (*models.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return m.config, m.err
}

func (m *mockConfigStore) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

type broadcastCall struct {
	SessionUUID uuid.UUID
	Value       float64
	Status      string
}

type mockBroadcaster struct {
	mu         sync.Mutex
	broadcasts []broadcastCall
}

func (m *mockBroadcaster) Broadcast(sessionUUID uuid.UUID, value float64, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcasts = append(m.broadcasts, broadcastCall{sessionUUID, value, status})
}

func (m *mockBroadcaster) getBroadcasts() []broadcastCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]broadcastCall, len(m.broadcasts))
	copy(cp, m.broadcasts)
	return cp
}

type mockTwitchManager struct {
	mu               sync.Mutex
	unsubscribeCalls []uuid.UUID
	unsubscribeErr   error
}

func (m *mockTwitchManager) Unsubscribe(ctx context.Context, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unsubscribeCalls = append(m.unsubscribeCalls, userID)
	return m.unsubscribeErr
}

func (m *mockTwitchManager) getUnsubscribeCalls() []uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]uuid.UUID, len(m.unsubscribeCalls))
	copy(cp, m.unsubscribeCalls)
	return cp
}

// --- Helpers ---

func defaultConfig() *models.Config {
	return &models.Config{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     0.5,
	}
}

type testEngine struct {
	engine      *Engine
	clock       *clockwork.FakeClock
	configStore *mockConfigStore
	broadcaster *mockBroadcaster
}

func newTestEngine(t *testing.T, cfg *models.Config) *testEngine {
	t.Helper()
	fakeClock := clockwork.NewFakeClock()
	configStore := &mockConfigStore{config: cfg}
	memStore := NewInMemoryStore(fakeClock)
	broadcaster := &mockBroadcaster{}
	engine := NewEngine(memStore, configStore, fakeClock, true)
	engine.broadcaster = broadcaster
	// Start the actor goroutine (required for command processing)
	go engine.run()
	t.Cleanup(func() {
		engine.Stop()
	})
	return &testEngine{
		engine:      engine,
		clock:       fakeClock,
		configStore: configStore,
		broadcaster: broadcaster,
	}
}

func (te *testEngine) activate(t *testing.T) (sessionUUID uuid.UUID, broadcasterID string) {
	t.Helper()
	sessionUUID = uuid.New()
	broadcasterID = "broadcaster_" + sessionUUID.String()[:8]
	err := te.engine.ActivateSession(context.Background(), sessionUUID, uuid.New(), broadcasterID)
	require.NoError(t, err)
	return sessionUUID, broadcasterID
}

// waitForValue sends a ProcessVote (fire-and-forget) and then reads GetSessionValue
// which acts as a barrier ensuring the vote has been processed.
func (te *testEngine) getValue(t *testing.T, sessionUUID uuid.UUID) float64 {
	t.Helper()
	value, ok := te.engine.GetSessionValue(sessionUUID)
	require.True(t, ok)
	return value
}

// --- ActivateSession Tests ---

func TestActivateSession_Success(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID := uuid.New()
	userID := uuid.New()

	err := te.engine.ActivateSession(context.Background(), sessionUUID, userID, "broadcaster123")
	require.NoError(t, err)

	cfg := te.engine.GetSessionConfig(sessionUUID)
	require.NotNil(t, cfg)
	assert.Equal(t, "yes", cfg.ForTrigger)
	assert.Equal(t, "no", cfg.AgainstTrigger)
	assert.Equal(t, "Against", cfg.LeftLabel)
	assert.Equal(t, "For", cfg.RightLabel)
	assert.Equal(t, 0.5, cfg.DecaySpeed)
}

func TestActivateSession_AlreadyActive(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID := uuid.New()
	userID := uuid.New()

	err := te.engine.ActivateSession(context.Background(), sessionUUID, userID, "broadcaster123")
	require.NoError(t, err)
	assert.Equal(t, 1, te.configStore.getCallCount())

	// Activate again — should be idempotent, no extra DB call
	err = te.engine.ActivateSession(context.Background(), sessionUUID, userID, "broadcaster123")
	require.NoError(t, err)
	assert.Equal(t, 1, te.configStore.getCallCount())
}

func TestActivateSession_ReconnectPreservesValue(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID := uuid.New()
	userID := uuid.New()

	err := te.engine.ActivateSession(context.Background(), sessionUUID, userID, "broadcaster123")
	require.NoError(t, err)

	// Accumulate some sentiment
	te.engine.ProcessVote("broadcaster123", "user1", "yes")
	te.engine.ProcessVote("broadcaster123", "user2", "yes")
	assert.Equal(t, 20.0, te.getValue(t, sessionUUID))

	// Simulate disconnect + reconnect
	te.engine.MarkDisconnected(sessionUUID)
	err = te.engine.ActivateSession(context.Background(), sessionUUID, userID, "broadcaster123")
	require.NoError(t, err)

	// Value must be preserved
	assert.Equal(t, 20.0, te.getValue(t, sessionUUID))

	// Session should NOT be cleaned up even after waiting past orphan threshold
	// (because ActivateSession cleared the disconnect timestamp)
	te.clock.Advance(60 * time.Second)
	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)
	// Use GetSessionConfig as barrier to ensure cleanup processed
	assert.NotNil(t, te.engine.GetSessionConfig(sessionUUID))
}

func TestActivateSession_DBError(t *testing.T) {
	te := newTestEngine(t, nil)
	te.configStore.err = fmt.Errorf("database connection refused")

	err := te.engine.ActivateSession(context.Background(), uuid.New(), uuid.New(), "broadcaster123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database connection refused")
}

// --- ProcessVote Tests ---

func TestProcessVote_ForTrigger(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.ProcessVote(broadcasterID, "user1", "I vote yes!")

	assert.Equal(t, 10.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_AgainstTrigger(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.ProcessVote(broadcasterID, "user1", "vote no")

	assert.Equal(t, -10.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_NoMatch(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.ProcessVote(broadcasterID, "user1", "hello world")

	assert.Equal(t, 0.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_CaseInsensitive(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.ProcessVote(broadcasterID, "user1", "YES PLEASE!")

	assert.Equal(t, 10.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_Debounce(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	// First vote — should apply
	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	assert.Equal(t, 10.0, te.getValue(t, sessionUUID))

	// Second vote immediately — should be debounced
	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	assert.Equal(t, 10.0, te.getValue(t, sessionUUID))

	// Advance clock past debounce window
	te.clock.Advance(2 * time.Second)

	// Third vote — should apply
	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	assert.Equal(t, 20.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_DifferentUsers(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	te.engine.ProcessVote(broadcasterID, "user2", "yes")

	assert.Equal(t, 20.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_Clamping(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	// 15 different users each vote "yes" (+10 each = 150, clamped to 100)
	for i := 0; i < 15; i++ {
		te.engine.ProcessVote(broadcasterID, fmt.Sprintf("user%d", i), "yes")
	}

	assert.Equal(t, 100.0, te.getValue(t, sessionUUID))
}

func TestProcessVote_NoActiveSession(t *testing.T) {
	te := newTestEngine(t, defaultConfig())

	// Should not panic
	te.engine.ProcessVote("unknown_broadcaster", "user1", "yes")
	// Use GetSessionConfig as a barrier to ensure the vote command was processed
	te.engine.GetSessionConfig(uuid.New())
}

func TestProcessVote_ForTriggerPriority(t *testing.T) {
	cfg := &models.Config{
		ForTrigger:     "yes",
		AgainstTrigger: "yesno",
		DecaySpeed:     0.5,
	}
	te := newTestEngine(t, cfg)
	sessionUUID, broadcasterID := te.activate(t)

	// "yesno" contains "yes" (ForTrigger), so ForTrigger matches first
	te.engine.ProcessVote(broadcasterID, "user1", "yesno")

	assert.Equal(t, 10.0, te.getValue(t, sessionUUID), "ForTrigger should take priority when both match")
}

// --- ResetSession Tests ---

func TestResetSession(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	te.engine.ProcessVote(broadcasterID, "user2", "yes")
	assert.Equal(t, 20.0, te.getValue(t, sessionUUID))

	te.engine.ResetSession(sessionUUID)
	assert.Equal(t, 0.0, te.getValue(t, sessionUUID))
}

func TestResetSession_Nonexistent(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	// Should not panic
	te.engine.ResetSession(uuid.New())
	// Barrier
	te.engine.GetSessionConfig(uuid.New())
}

// --- MarkDisconnected Tests ---

func TestMarkDisconnected(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, _ := te.activate(t)

	te.engine.MarkDisconnected(sessionUUID)
	// Barrier: ensure MarkDisconnected is processed before advancing clock
	te.engine.GetSessionConfig(sessionUUID)

	// Verify disconnect was recorded by checking cleanup behavior:
	// advance past orphan threshold and verify session gets cleaned up
	te.clock.Advance(31 * time.Second)
	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)
	// Barrier
	assert.Nil(t, te.engine.GetSessionConfig(sessionUUID))
}

// --- CleanupOrphans Tests ---

func TestCleanupOrphans_RemovesOld(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, _ := te.activate(t)

	te.engine.MarkDisconnected(sessionUUID)
	te.engine.GetSessionConfig(sessionUUID) // barrier
	te.clock.Advance(31 * time.Second)

	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)

	assert.Nil(t, te.engine.GetSessionConfig(sessionUUID))
}

func TestCleanupOrphans_KeepsReconnected(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, _ := te.activate(t)

	// Client disconnects
	te.engine.MarkDisconnected(sessionUUID)
	te.engine.GetSessionConfig(sessionUUID) // barrier
	te.clock.Advance(1 * time.Second)

	// Client reconnects — ActivateSession clears LastClientDisconnect
	err := te.engine.ActivateSession(context.Background(), sessionUUID, uuid.New(), "broadcaster_reconnect")
	require.NoError(t, err)

	// Wait well past the orphan threshold
	te.clock.Advance(60 * time.Second)

	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)

	// Session must still be alive
	assert.NotNil(t, te.engine.GetSessionConfig(sessionUUID))
	assert.Empty(t, tm.getUnsubscribeCalls())
}

func TestCleanupOrphans_KeepsRecent(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, _ := te.activate(t)

	te.engine.MarkDisconnected(sessionUUID)
	te.engine.GetSessionConfig(sessionUUID) // barrier
	te.clock.Advance(5 * time.Second)

	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)

	assert.NotNil(t, te.engine.GetSessionConfig(sessionUUID))
}

func TestCleanupOrphans_Unsubscribes(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, _ := te.activate(t)

	te.engine.MarkDisconnected(sessionUUID)
	te.engine.GetSessionConfig(sessionUUID) // barrier
	te.clock.Advance(31 * time.Second)

	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)

	// Barrier to ensure cleanup command processed
	te.engine.GetSessionConfig(uuid.New())
	// Give goroutine time to call Unsubscribe
	time.Sleep(50 * time.Millisecond)

	calls := tm.getUnsubscribeCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, sessionUUID, calls[0])
}

func TestCleanupOrphans_OnlyRemovesOrphans(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	orphanUUID, _ := te.activate(t)
	activeUUID, activeBroadcaster := te.activate(t)

	// Only orphan the first session
	te.engine.MarkDisconnected(orphanUUID)
	te.engine.GetSessionConfig(orphanUUID) // barrier
	te.clock.Advance(31 * time.Second)

	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)

	// Orphan removed, active session untouched
	assert.Nil(t, te.engine.GetSessionConfig(orphanUUID))
	assert.NotNil(t, te.engine.GetSessionConfig(activeUUID))

	// Active session still processes votes
	te.engine.ProcessVote(activeBroadcaster, "user1", "yes")
	assert.Equal(t, 10.0, te.getValue(t, activeUUID))
}

func TestCleanupOrphans_BroadcasterMappingCleaned(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	te.engine.MarkDisconnected(sessionUUID)
	te.engine.GetSessionConfig(sessionUUID) // barrier
	te.clock.Advance(31 * time.Second)

	tm := &mockTwitchManager{}
	te.engine.CleanupOrphans(tm)

	// Votes for the cleaned-up broadcaster should be silently dropped
	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	assert.Nil(t, te.engine.GetSessionConfig(sessionUUID))
}

// --- Edge Case Tests ---

func TestMarkDisconnected_NonexistentSession(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	// Should not panic
	te.engine.MarkDisconnected(uuid.New())
	// Barrier
	te.engine.GetSessionConfig(uuid.New())
}

func TestUpdateSessionConfig_NonexistentSession(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	// Should not panic
	te.engine.UpdateSessionConfig(uuid.New(), models.ConfigSnapshot{
		ForTrigger: "test",
	})
	// Barrier
	te.engine.GetSessionConfig(uuid.New())
}

// --- Config Tests ---

func TestUpdateSessionConfig(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, _ := te.activate(t)

	newSnapshot := models.ConfigSnapshot{
		ForTrigger:     "pog",
		AgainstTrigger: "notpog",
		LeftLabel:      "Bad",
		RightLabel:     "Good",
		DecaySpeed:     1.5,
	}
	te.engine.UpdateSessionConfig(sessionUUID, newSnapshot)

	cfg := te.engine.GetSessionConfig(sessionUUID)
	require.NotNil(t, cfg)
	assert.Equal(t, "pog", cfg.ForTrigger)
	assert.Equal(t, "notpog", cfg.AgainstTrigger)
	assert.Equal(t, "Bad", cfg.LeftLabel)
	assert.Equal(t, "Good", cfg.RightLabel)
	assert.Equal(t, 1.5, cfg.DecaySpeed)
}

func TestGetSessionConfig_Nonexistent(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	assert.Nil(t, te.engine.GetSessionConfig(uuid.New()))
}

// --- Ticker Loop Tests ---

func waitForBroadcasts(b *mockBroadcaster, minCount int) []broadcastCall {
	// Poll briefly for broadcasts to appear (goroutine needs time to process after clock advances)
	for range 50 {
		if casts := b.getBroadcasts(); len(casts) >= minCount {
			return casts
		}
		time.Sleep(time.Millisecond)
	}
	return b.getBroadcasts()
}

func TestTickerLoop_AppliesDecay(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID, broadcasterID := te.activate(t)

	// Set a value
	te.engine.ProcessVote(broadcasterID, "user1", "yes")
	assert.Equal(t, 10.0, te.getValue(t, sessionUUID))

	// Start ticker and let it fire
	go te.engine.tickerLoop()
	te.clock.BlockUntilContext(context.Background(), 1) //nolint:errcheck // Wait for ticker goroutine to be blocked on clock
	te.clock.Advance(50 * time.Millisecond)

	broadcasts := waitForBroadcasts(te.broadcaster, 1)
	require.NotEmpty(t, broadcasts)

	// Value should have decayed (multiplied by exp(-0.5 * 0.05) ≈ 0.9753)
	lastBroadcast := broadcasts[len(broadcasts)-1]
	assert.Equal(t, sessionUUID, lastBroadcast.SessionUUID)
	assert.Less(t, lastBroadcast.Value, 10.0)
	assert.Greater(t, lastBroadcast.Value, 9.0)
}

func TestTickerLoop_BroadcastsAllSessions(t *testing.T) {
	te := newTestEngine(t, defaultConfig())
	sessionUUID1, broadcasterID1 := te.activate(t)
	sessionUUID2, broadcasterID2 := te.activate(t)

	te.engine.ProcessVote(broadcasterID1, "user1", "yes")
	te.engine.ProcessVote(broadcasterID2, "user2", "yes")
	// Barrier to ensure votes are processed
	te.getValue(t, sessionUUID1)

	go te.engine.tickerLoop()
	te.clock.BlockUntilContext(context.Background(), 1) //nolint:errcheck
	te.clock.Advance(50 * time.Millisecond)

	broadcasts := waitForBroadcasts(te.broadcaster, 2)
	require.GreaterOrEqual(t, len(broadcasts), 2)

	// Collect broadcast session UUIDs
	broadcastedSessions := make(map[uuid.UUID]bool)
	for _, b := range broadcasts {
		broadcastedSessions[b.SessionUUID] = true
	}
	assert.True(t, broadcastedSessions[sessionUUID1], "session 1 should have been broadcast")
	assert.True(t, broadcastedSessions[sessionUUID2], "session 2 should have been broadcast")
}
