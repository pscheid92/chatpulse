package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// mockScaleProvider returns a fixed value for all sessions.
type mockScaleProvider struct {
	mu    sync.Mutex
	value float64
}

func (m *mockScaleProvider) GetCurrentValue(_ context.Context, _ uuid.UUID) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.value, nil
}

func (m *mockScaleProvider) setValue(v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.value = v
}

// testBroadcaster sets up an OverlayBroadcaster with a test HTTP server.
func testBroadcaster(t *testing.T, engine *mockScaleProvider, onSessionEmpty func(uuid.UUID)) (*OverlayBroadcaster, func(sessionUUID uuid.UUID) *ws.Conn) {
	t.Helper()

	if engine == nil {
		engine = &mockScaleProvider{}
	}

	broadcaster := NewOverlayBroadcaster(engine, nil, onSessionEmpty, clockwork.NewRealClock())
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

func waitForClientCount(b *OverlayBroadcaster, sessionUUID uuid.UUID, expected int) bool {
	for range 100 {
		if b.GetClientCount(sessionUUID) == expected {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestBroadcaster_RegisterAndReceiveTick(t *testing.T) {
	engine := &mockScaleProvider{value: 42.5}
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
	engine := &mockScaleProvider{value: 77.0}
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
	engine := &mockScaleProvider{}
	broadcaster := NewOverlayBroadcaster(engine, nil, nil, clockwork.NewRealClock())
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
	engine := &mockScaleProvider{value: 50.0}
	_ = NewOverlayBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	// Just verify no panic with ticks running and no clients
	time.Sleep(100 * time.Millisecond)
}

func TestBroadcaster_ValueUpdates(t *testing.T) {
	engine := &mockScaleProvider{value: 10.0}
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

// Suppress unused import warning for fmt
var _ = fmt.Sprintf
