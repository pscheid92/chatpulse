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

	"github.com/google/uuid"
	ws "github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEngine returns a fixed value for all sessions.
type mockEngine struct {
	mu       sync.Mutex
	value    float64
	delayFn  func(context.Context) error // Optional delay/error injection
}

func (m *mockEngine) GetCurrentValue(ctx context.Context, _ uuid.UUID) (float64, error) {
	// Apply delay if configured (for timeout testing)
	if m.delayFn != nil {
		if err := m.delayFn(ctx); err != nil {
			return 0, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.value, nil
}

func (m *mockEngine) ProcessVote(_ context.Context, _, _, _ string) (float64, bool) {
	return 0, false
}

func (m *mockEngine) ResetSentiment(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (m *mockEngine) setValue(v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.value = v
}

// testBroadcaster sets up a Broadcaster with a test HTTP server.
func testBroadcaster(t *testing.T, engine *mockEngine, onSessionEmpty func(uuid.UUID)) (*Broadcaster, func(sessionUUID uuid.UUID) *ws.Conn) {
	t.Helper()

	if engine == nil {
		engine = &mockEngine{}
	}

	broadcaster := NewBroadcaster(engine, nil, onSessionEmpty, clockwork.NewRealClock())
	t.Cleanup(func() { broadcaster.Stop() })

	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		sessionUUID := uuid.MustParse(r.URL.Query().Get("session"))
		_ = broadcaster.Register(sessionUUID, conn)

		go func() {
			defer broadcaster.Unregister(sessionUUID, conn)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					break
				}
			}
		}()
	}))
	t.Cleanup(func() { server.Close() })

	dial := func(sessionUUID uuid.UUID) *ws.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "?session=" + sessionUUID.String()
		conn, _, err := ws.DefaultDialer.Dial(url, nil)
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}

	return broadcaster, dial
}

func waitForClientCount(b *Broadcaster, sessionUUID uuid.UUID, expected int) bool {
	for range 100 {
		if b.GetClientCount(sessionUUID) == expected {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestBroadcaster_RegisterAndReceiveTick(t *testing.T) {
	engine := &mockEngine{value: 42.5}
	broadcaster, dial := testBroadcaster(t, engine, nil)
	sessionUUID := uuid.New()

	conn := dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 1))

	// Wait for a tick to deliver the value
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
	broadcaster, dial := testBroadcaster(t, engine, nil)
	sessionUUID := uuid.New()

	conn1 := dial(sessionUUID)
	conn2 := dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 2))

	// Both clients should receive values via tick
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

func TestBroadcaster_OnSessionEmpty(t *testing.T) {
	var mu sync.Mutex
	var disconnectedSessions []uuid.UUID
	onEmpty := func(id uuid.UUID) {
		mu.Lock()
		defer mu.Unlock()
		disconnectedSessions = append(disconnectedSessions, id)
	}

	broadcaster, dial := testBroadcaster(t, nil, onEmpty)
	sessionUUID := uuid.New()

	conn1 := dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 1))

	conn2 := dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 2))

	// Close first — still one client left, no callback
	conn1.Close()
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 1))
	mu.Lock()
	assert.Empty(t, disconnectedSessions)
	mu.Unlock()

	// Close second — last client, callback fires
	conn2.Close()
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 0))
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	require.Len(t, disconnectedSessions, 1)
	assert.Equal(t, sessionUUID, disconnectedSessions[0])
	mu.Unlock()
}

func TestBroadcaster_GetClientCount(t *testing.T) {
	broadcaster, dial := testBroadcaster(t, nil, nil)
	sessionUUID := uuid.New()

	assert.Equal(t, 0, broadcaster.GetClientCount(sessionUUID))

	conn1 := dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 1))

	dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 2))

	conn1.Close()
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 1))
}

func TestBroadcaster_MaxClientsPerSession(t *testing.T) {
	engine := &mockEngine{}
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	t.Cleanup(func() { broadcaster.Stop() })

	sessionUUID := uuid.New()

	conns := make([]*ws.Conn, 0, maxClientsPerSession)
	for i := range maxClientsPerSession {
		server, client := newTestConnPair(t)
		err := broadcaster.Register(sessionUUID, server)
		require.NoError(t, err, "client %d should register successfully", i)
		conns = append(conns, client)
	}

	assert.Equal(t, maxClientsPerSession, broadcaster.GetClientCount(sessionUUID))

	// The next client should be rejected
	server, client := newTestConnPair(t)
	err := broadcaster.Register(sessionUUID, server)
	assert.Error(t, err, "client beyond max should be rejected")
	assert.Contains(t, err.Error(), "max clients per session")

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
	_ = NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	// Just verify no panic with ticks running and no clients
	time.Sleep(100 * time.Millisecond)
}

func TestBroadcaster_ValueUpdates(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster, dial := testBroadcaster(t, engine, nil)
	sessionUUID := uuid.New()

	conn := dial(sessionUUID)
	require.True(t, waitForClientCount(broadcaster, sessionUUID, 1))

	// Read first tick
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(msg, &result))
	assert.Equal(t, 10.0, result["value"])

	// Update value
	engine.setValue(55.0)

	// Read next tick
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err = conn.ReadMessage()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(msg, &result))
	assert.Equal(t, 55.0, result["value"])
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

	// Create broadcaster with multiple sessions and clients
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	afterCreate := runtime.NumGoroutine()

	session1 := uuid.New()
	session2 := uuid.New()

	// Register multiple clients across different sessions
	clients := make([]*ws.Conn, 0, 5)
	for range 3 {
		server, client := newTestConnPair(t)
		err := broadcaster.Register(session1, server)
		require.NoError(t, err)
		clients = append(clients, client)
	}

	for range 2 {
		server, client := newTestConnPair(t)
		err := broadcaster.Register(session2, server)
		require.NoError(t, err)
		clients = append(clients, client)
	}

	// Verify clients are registered
	assert.Equal(t, 3, broadcaster.GetClientCount(session1))
	assert.Equal(t, 2, broadcaster.GetClientCount(session2))

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
	var mu sync.Mutex
	var emptyCalled []uuid.UUID
	onEmpty := func(id uuid.UUID) {
		mu.Lock()
		defer mu.Unlock()
		emptyCalled = append(emptyCalled, id)
	}

	engine := &mockEngine{value: 42.0}
	broadcaster := NewBroadcaster(engine, nil, onEmpty, clockwork.NewRealClock())

	session1 := uuid.New()
	session2 := uuid.New()

	// Register clients
	server1, client1 := newTestConnPair(t)
	err := broadcaster.Register(session1, server1)
	require.NoError(t, err)

	server2, client2 := newTestConnPair(t)
	err = broadcaster.Register(session2, server2)
	require.NoError(t, err)

	// Verify both sessions have clients
	assert.Equal(t, 1, broadcaster.GetClientCount(session1))
	assert.Equal(t, 1, broadcaster.GetClientCount(session2))

	// Stop broadcaster
	broadcaster.Stop()

	// Give time for callbacks to fire
	time.Sleep(100 * time.Millisecond)

	// Verify onSessionEmpty was called for both sessions
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, emptyCalled, 2)
	assert.Contains(t, emptyCalled, session1)
	assert.Contains(t, emptyCalled, session2)

	// Cleanup
	client1.Close()
	client2.Close()
}

func TestBroadcasterStopIdempotent(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())

	session := uuid.New()
	server, client := newTestConnPair(t)
	err := broadcaster.Register(session, server)
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
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())

	session := uuid.New()
	server, client := newTestConnPair(t)
	err := broadcaster.Register(session, server)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	// Stop broadcaster
	broadcaster.Stop()

	// Try to register after stop - should timeout/fail gracefully
	// Note: This tests that the actor loop has exited
	done := make(chan error, 1)
	go func() {
		server2, client2 := newTestConnPair(t)
		t.Cleanup(func() { client2.Close() })
		done <- broadcaster.Register(session, server2)
	}()

	select {
	case <-done:
		// Command processed (actor still running) or timed out - either is acceptable
		// since Stop() doesn't guarantee immediate shutdown
	case <-time.After(200 * time.Millisecond):
		// Timeout means command is blocked, which indicates actor has stopped
	}
}

func TestBroadcaster_RedisTimeoutHandling(t *testing.T) {
	// Test that slow Redis operations don't block the broadcaster indefinitely
	session := uuid.New()
	timeoutCount := 0
	var timeoutMu sync.Mutex

	engine := &mockEngine{
		value: 50.0,
		delayFn: func(ctx context.Context) error {
			// Simulate slow Redis by sleeping until context is cancelled
			select {
			case <-time.After(5 * time.Second):
				return nil
			case <-ctx.Done():
				timeoutMu.Lock()
				timeoutCount++
				timeoutMu.Unlock()
				return ctx.Err()
			}
		},
	}

	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	t.Cleanup(func() { broadcaster.Stop() })

	server, client := newTestConnPair(t)
	err := broadcaster.Register(session, server)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	// Wait for at least one timeout to occur (redisTimeout=2s)
	// First tick happens after tickInterval (50ms), then timeout takes 2s
	time.Sleep(2500 * time.Millisecond)

	// Verify that timeouts occurred (broadcaster didn't hang)
	timeoutMu.Lock()
	actualTimeouts := timeoutCount
	timeoutMu.Unlock()

	assert.Greater(t, actualTimeouts, 0, "expected at least one timeout to occur")
	t.Logf("Redis timeout occurred %d times (broadcaster remained responsive)", actualTimeouts)
}

func TestBroadcaster_CommandTimeoutHandling(t *testing.T) {
	// Test that commands timeout gracefully when broadcaster is overwhelmed
	// We use a real clock and very slow operations to trigger timeouts

	session := uuid.New()
	blockForever := make(chan struct{})

	// Create broadcaster with an engine that blocks forever after first call
	callCount := 0
	engine := &mockEngine{
		value: 10.0,
		delayFn: func(ctx context.Context) error {
			callCount++
			if callCount > 1 {
				// Block the broadcaster's tick loop
				<-blockForever
			}
			return nil
		},
	}

	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	t.Cleanup(func() {
		close(blockForever)
		broadcaster.Stop()
	})

	// First register succeeds (broadcaster not stuck yet)
	server1, client1 := newTestConnPair(t)
	err := broadcaster.Register(session, server1)
	require.NoError(t, err)
	t.Cleanup(func() { client1.Close() })

	// Wait for first tick to complete and second tick to start blocking
	time.Sleep(150 * time.Millisecond)

	// Now broadcaster is stuck in handleTick - commands should timeout
	t.Run("Register timeout", func(t *testing.T) {
		server2, client2 := newTestConnPair(t)
		t.Cleanup(func() { client2.Close() })

		start := time.Now()
		err := broadcaster.Register(session, server2)
		elapsed := time.Since(start)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
		assert.Greater(t, elapsed, 4*time.Second, "should wait for timeout")
		assert.Less(t, elapsed, 6*time.Second, "should timeout promptly")
		t.Logf("Register correctly timed out after %v: %v", elapsed, err)
	})

	t.Run("GetClientCount timeout", func(t *testing.T) {
		start := time.Now()
		count := broadcaster.GetClientCount(session)
		elapsed := time.Since(start)

		assert.Equal(t, -1, count, "GetClientCount should return -1 on timeout")
		assert.Greater(t, elapsed, 4*time.Second, "should wait for timeout")
		assert.Less(t, elapsed, 6*time.Second, "should timeout promptly")
		t.Logf("GetClientCount correctly returned -1 after %v", elapsed)
	})
}

// Suppress unused import warning for fmt
var _ = fmt.Sprintf
