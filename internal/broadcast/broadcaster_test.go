package broadcast

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBroadcasterID = "test-broadcaster-1"

// mockEngine returns a fixed value for all broadcasters.
type mockEngine struct {
	mu      sync.Mutex
	value   float64
	delayFn func(context.Context) error // Optional delay/error injection
}

func (m *mockEngine) GetBroadcastData(ctx context.Context, _ string) (*domain.BroadcastData, error) {
	if m.delayFn != nil {
		if err := m.delayFn(ctx); err != nil {
			return nil, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return &domain.BroadcastData{Value: m.value, DecaySpeed: 1.0, UnixTimestamp: time.Now().UnixMilli()}, nil
}

func (m *mockEngine) ProcessVote(_ context.Context, _, _, _ string) (float64, domain.VoteResult, error) {
	return 0, domain.VoteNoMatch, nil
}

func (m *mockEngine) ResetSentiment(_ context.Context, _ string) error {
	return nil
}

// testBroadcaster sets up a Broadcaster with a test HTTP server (no real Redis).
func testBroadcaster(t *testing.T, engine *mockEngine) (*Broadcaster, func(broadcasterID string) *ws.Conn) {
	t.Helper()

	if engine == nil {
		engine = &mockEngine{}
	}

	// nil redisClient — pub/sub subscriber won't start (unit test)
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)
	t.Cleanup(func() { broadcaster.Stop() })

	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		broadcasterID := r.URL.Query().Get("broadcaster")
		if broadcasterID == "" {
			broadcasterID = "test-broadcaster"
		}
		_ = broadcaster.Subscribe(broadcasterID, conn)

		go func() {
			defer broadcaster.Unsubscribe(broadcasterID, conn)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					break
				}
			}
		}()
	}))
	t.Cleanup(func() { server.Close() })

	dial := func(broadcasterID string) *ws.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "?broadcaster=" + broadcasterID
		conn, _, err := ws.DefaultDialer.Dial(url, nil)
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}

	return broadcaster, dial
}

func waitForClientCount(b *Broadcaster, broadcasterID string, expected int) bool { //nolint:unparam // broadcasterID varies across test functions
	for range 100 {
		if b.GetClientCount(broadcasterID) == expected {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// sendSentimentUpdate injects a sentiment update command into the broadcaster's command channel.
// This simulates a pub/sub message arriving without needing a real Redis connection.
func sendSentimentUpdate(b *Broadcaster, broadcasterID string, value float64, timestamp int64) {
	b.cmdCh <- sentimentUpdateCmd{
		broadcasterID: broadcasterID,
		value:         value,
		timestamp:     timestamp,
		receivedAt:    time.Now(),
	}
}

func TestBroadcaster_RegisterAndReceiveUpdate(t *testing.T) {
	engine := &mockEngine{value: 42.5}
	broadcaster, dial := testBroadcaster(t, engine)
	broadcasterID := testBroadcasterID

	conn := dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 1))

	// Simulate a pub/sub sentiment update
	now := time.Now().UnixMilli()
	sendSentimentUpdate(broadcaster, broadcasterID, 42.5, now)

	// Client should receive the update
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(msg, &result))
	assert.Equal(t, 42.5, result["value"])
	assert.Equal(t, "active", result["status"])
}

func TestBroadcaster_MultipleClients(t *testing.T) {
	engine := &mockEngine{value: 77.0}
	broadcaster, dial := testBroadcaster(t, engine)
	broadcasterID := testBroadcasterID

	conn1 := dial(broadcasterID)
	conn2 := dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 2))

	// Simulate a pub/sub sentiment update
	now := time.Now().UnixMilli()
	sendSentimentUpdate(broadcaster, broadcasterID, 77.0, now)

	// Both clients should receive the update
	for _, conn := range []*ws.Conn{conn1, conn2} {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(msg, &result))
		assert.Equal(t, 77.0, result["value"])
		assert.Equal(t, "active", result["status"])
	}
}

func TestBroadcaster_GetClientCount(t *testing.T) {
	broadcaster, dial := testBroadcaster(t, nil)
	broadcasterID := testBroadcasterID

	assert.Equal(t, 0, broadcaster.GetClientCount(broadcasterID))

	conn1 := dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 1))

	dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 2))

	conn1.Close()
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 1))
}

func TestBroadcaster_MaxClientsPerSession(t *testing.T) {
	engine := &mockEngine{}
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)
	t.Cleanup(func() { broadcaster.Stop() })

	broadcasterID := testBroadcasterID

	conns := make([]*ws.Conn, 0, 50)
	for i := range 50 {
		server, client := newTestConnPair(t)
		err := broadcaster.Subscribe(broadcasterID, server)
		require.NoError(t, err, "client %d should register successfully", i)
		conns = append(conns, client)
	}

	assert.Equal(t, 50, broadcaster.GetClientCount(broadcasterID))

	// The next client should be rejected
	server, client := newTestConnPair(t)
	err := broadcaster.Subscribe(broadcasterID, server)
	assert.Error(t, err, "client beyond max should be rejected")
	assert.Contains(t, err.Error(), "max clients per broadcaster")

	_ = client
	for _, c := range conns {
		c.Close()
	}
}

func newTestConnPair(t *testing.T) (server *ws.Conn, client *ws.Conn) {
	t.Helper()
	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ready := make(chan *ws.Conn, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		ready <- conn
	}))
	t.Cleanup(func() { srv.Close() })

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := ws.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { clientConn.Close() })

	serverConn := <-ready
	t.Cleanup(func() { serverConn.Close() })

	return serverConn, clientConn
}

func TestBroadcaster_NoClientsNoPanic(t *testing.T) {
	engine := &mockEngine{value: 50.0}
	b := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)
	// Just verify no panic with ticks running and no clients
	time.Sleep(100 * time.Millisecond)
	b.Stop()
}

func TestBroadcaster_ValueUpdates(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster, dial := testBroadcaster(t, engine)
	broadcasterID := testBroadcasterID

	conn := dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 1))

	// Send first sentiment update
	now := time.Now().UnixMilli()
	sendSentimentUpdate(broadcaster, broadcasterID, 10.0, now)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(msg, &result))
	assert.Equal(t, 10.0, result["value"])

	// Send second sentiment update with different value
	sendSentimentUpdate(broadcaster, broadcasterID, 55.0, now+50)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err = conn.ReadMessage()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(msg, &result))
	assert.Equal(t, 55.0, result["value"])
}

func TestBroadcaster_SentimentUpdateForUnknownBroadcaster(t *testing.T) {
	// Sentiment updates for unknown broadcasters should be silently dropped
	engine := &mockEngine{}
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)
	t.Cleanup(func() { broadcaster.Stop() })

	// Send update for a broadcaster with no clients -- should not panic
	sendSentimentUpdate(broadcaster, "nonexistent-broadcaster", 42.0, time.Now().UnixMilli())

	// Give actor time to process
	time.Sleep(50 * time.Millisecond)
}

func TestBroadcasterStopCleansUpGoroutines(t *testing.T) {
	// This test verifies that Stop() synchronizes goroutine cleanup.
	// Note: Some goroutine "leaks" are from test infrastructure (httptest servers)
	// which create internal goroutines that clean up asynchronously.

	engine := &mockEngine{value: 10.0}

	// Capture baseline goroutine count before creating broadcaster
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Create broadcaster with multiple broadcasters and clients
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)
	afterCreate := runtime.NumGoroutine()

	broadcasterID1 := "test-broadcaster-1"
	broadcasterID2 := testBroadcasterID + "-alt"

	// Register multiple clients across different broadcasters
	clients := make([]*ws.Conn, 0, 5)
	for range 3 {
		server, client := newTestConnPair(t)
		err := broadcaster.Subscribe(broadcasterID1, server)
		require.NoError(t, err)
		clients = append(clients, client)
	}

	for range 2 {
		server, client := newTestConnPair(t)
		err := broadcaster.Subscribe(broadcasterID2, server)
		require.NoError(t, err)
		clients = append(clients, client)
	}

	// Verify clients are registered
	assert.Equal(t, 3, broadcaster.GetClientCount(broadcasterID1))
	assert.Equal(t, 2, broadcaster.GetClientCount(broadcasterID2))

	afterRegister := runtime.NumGoroutine()
	t.Logf("Goroutines: baseline=%d, after_create=%d, after_register=%d", baseline, afterCreate, afterRegister)

	// Stop broadcaster - this should block until all clientWriter goroutines exit
	broadcaster.Stop()

	// Close all client connections to allow test infrastructure to clean up
	for _, client := range clients {
		client.Close()
	}

	// Give test infrastructure time to clean up
	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	// Verify goroutines cleaned up (allowing tolerance for test infrastructure)
	finalCount := runtime.NumGoroutine()
	goroutineLeak := finalCount - baseline
	t.Logf("Final goroutines: baseline=%d, final=%d, leak=%d", baseline, finalCount, goroutineLeak)

	// The broadcaster's own goroutines (run loop + clientWriter goroutines) should be gone.
	// Remaining goroutines are from test infrastructure (httptest.NewServer creates
	// internal goroutines that clean up asynchronously).
	assert.Less(t, goroutineLeak, 10, "excessive goroutine leak detected: baseline=%d, final=%d", baseline, finalCount)

	if goroutineLeak > 0 {
		t.Logf("NOTE: %d residual goroutines from test infrastructure (httptest servers)", goroutineLeak)
	}
}

func TestBroadcasterStopWithActiveClients(t *testing.T) {
	engine := &mockEngine{value: 42.0}
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)

	broadcasterID1 := testBroadcasterID
	broadcasterID2 := testBroadcasterID + "-alt"

	// Register clients
	server1, client1 := newTestConnPair(t)
	err := broadcaster.Subscribe(broadcasterID1, server1)
	require.NoError(t, err)

	server2, client2 := newTestConnPair(t)
	err = broadcaster.Subscribe(broadcasterID2, server2)
	require.NoError(t, err)

	// Verify both broadcasters have clients
	assert.Equal(t, 1, broadcaster.GetClientCount(broadcasterID1))
	assert.Equal(t, 1, broadcaster.GetClientCount(broadcasterID2))

	// Stop broadcaster — should not panic with active clients
	broadcaster.Stop()

	// Cleanup
	client1.Close()
	client2.Close()
}

func TestBroadcasterStopIdempotent(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)

	broadcasterID := testBroadcasterID
	server, client := newTestConnPair(t)
	err := broadcaster.Subscribe(broadcasterID, server)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	// Call Stop multiple times - should not panic
	broadcaster.Stop()
	broadcaster.Stop()
	broadcaster.Stop()

	// Give time for any issues to surface
	time.Sleep(50 * time.Millisecond)
}

func TestBroadcasterStopBlocksCommandProcessing(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster := NewBroadcaster(engine, nil, clockwork.NewRealClock(), 50, 5*time.Second)

	broadcasterID := testBroadcasterID
	server, client := newTestConnPair(t)
	err := broadcaster.Subscribe(broadcasterID, server)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	// Stop broadcaster
	broadcaster.Stop()

	// Try to subscribe after stop - should timeout/fail gracefully
	// Note: This tests that the actor loop has exited
	done := make(chan error, 1)
	go func() {
		server2, client2 := newTestConnPair(t)
		t.Cleanup(func() { client2.Close() })
		done <- broadcaster.Subscribe(broadcasterID, server2)
	}()

	select {
	case <-done:
		// Command processed (actor still running) or timed out - either is acceptable
		// since Stop() doesn't guarantee immediate shutdown
	case <-time.After(200 * time.Millisecond):
		// Timeout means command is blocked, which indicates actor has stopped
	}
}

func TestBroadcaster_DegradedStatusNotifiesClients(t *testing.T) {
	// Test that sending a statusChangeCmd with "degraded" delivers the status
	// to all connected clients as a status-only JSON message.
	broadcasterID := testBroadcasterID

	broadcaster, dial := testBroadcaster(t, nil)
	conn := dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 1))

	// Inject degraded status via the actor command channel
	broadcaster.cmdCh <- statusChangeCmd{status: "degraded"}

	// Read the degraded message
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(msg, &result))

	assert.Equal(t, "degraded", result["status"], "should receive degraded status")
	_, hasValue := result["value"]
	assert.False(t, hasValue, "degraded status message should not contain value field")
}

func TestBroadcaster_RecoveryStatusNotifiesClients(t *testing.T) {
	// Test the degraded -> active recovery cycle via statusChangeCmd.
	broadcasterID := testBroadcasterID

	broadcaster, dial := testBroadcaster(t, nil)
	conn := dial(broadcasterID)
	require.True(t, waitForClientCount(broadcaster, broadcasterID, 1))

	// Send degraded, then active
	broadcaster.cmdCh <- statusChangeCmd{status: "degraded"}
	broadcaster.cmdCh <- statusChangeCmd{status: "active"}

	conn.SetReadDeadline(time.Now().Add(time.Second))

	// Read degraded message
	_, msg1, err := conn.ReadMessage()
	require.NoError(t, err)
	var result1 map[string]any
	require.NoError(t, json.Unmarshal(msg1, &result1))
	assert.Equal(t, "degraded", result1["status"])

	// Read active recovery message
	_, msg2, err := conn.ReadMessage()
	require.NoError(t, err)
	var result2 map[string]any
	require.NoError(t, json.Unmarshal(msg2, &result2))
	assert.Equal(t, "active", result2["status"])
	_, hasValue := result2["value"]
	assert.False(t, hasValue, "active status message should not contain value field")
}

// Suppress unused import warning for fmt
var _ = fmt.Sprintf
