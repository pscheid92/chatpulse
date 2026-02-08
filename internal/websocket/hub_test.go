package websocket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	ws "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testHub sets up a Hub with a test HTTP server that upgrades connections to WebSocket.
// Returns the hub, a dial function to connect clients, and a cleanup function.
func testHub(t *testing.T, onFirst func(uuid.UUID) error, onLast func(uuid.UUID)) (*Hub, func(sessionUUID uuid.UUID) *ws.Conn) {
	t.Helper()

	hub := NewHub(onFirst, onLast)
	t.Cleanup(func() { hub.Stop() })

	upgrader := ws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		sessionUUID := uuid.MustParse(r.URL.Query().Get("session"))
		_ = hub.Register(sessionUUID, conn)

		// Read loop to detect disconnects
		go func() {
			defer hub.Unregister(sessionUUID, conn)
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

	return hub, dial
}

// waitForClientCount polls until the hub has the expected count for a session.
func waitForClientCount(hub *Hub, sessionUUID uuid.UUID, expected int) bool {
	for range 100 {
		if hub.GetClientCount(sessionUUID) == expected {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestHub_RegisterAndBroadcast(t *testing.T) {
	hub, dial := testHub(t, nil, nil)
	sessionUUID := uuid.New()

	conn := dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 1))

	hub.Broadcast(sessionUUID, 42.5, "active")

	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(msg, &result))
	assert.Equal(t, 42.5, result["value"])
	assert.Equal(t, "active", result["status"])
}

func TestHub_MultipleClients(t *testing.T) {
	hub, dial := testHub(t, nil, nil)
	sessionUUID := uuid.New()

	conn1 := dial(sessionUUID)
	conn2 := dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 2))

	hub.Broadcast(sessionUUID, 77.0, "active")

	// Both clients should receive the message
	for _, conn := range []*ws.Conn{conn1, conn2} {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)

		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(msg, &result))
		assert.Equal(t, 77.0, result["value"])
		assert.Equal(t, "active", result["status"])
	}
}

func TestHub_OnFirstConnect(t *testing.T) {
	var connectCount atomic.Int32
	onFirst := func(id uuid.UUID) error {
		connectCount.Add(1)
		return nil
	}

	hub, dial := testHub(t, onFirst, nil)
	sessionUUID := uuid.New()

	// First client — triggers onFirstConnect
	dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 1))
	assert.Equal(t, int32(1), connectCount.Load())

	// Second client — should NOT trigger onFirstConnect
	dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 2))
	assert.Equal(t, int32(1), connectCount.Load())
}

func TestHub_OnLastDisconnect(t *testing.T) {
	var mu sync.Mutex
	var disconnectedSessions []uuid.UUID
	onLast := func(id uuid.UUID) {
		mu.Lock()
		defer mu.Unlock()
		disconnectedSessions = append(disconnectedSessions, id)
	}

	hub, dial := testHub(t, nil, onLast)
	sessionUUID := uuid.New()

	conn1 := dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 1))

	conn2 := dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 2))

	// Close first — still one client left, no callback
	conn1.Close()
	require.True(t, waitForClientCount(hub, sessionUUID, 1))
	mu.Lock()
	assert.Empty(t, disconnectedSessions)
	mu.Unlock()

	// Close second — last client, callback fires
	conn2.Close()
	require.True(t, waitForClientCount(hub, sessionUUID, 0))
	// Wait a bit for the callback to fire
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	require.Len(t, disconnectedSessions, 1)
	assert.Equal(t, sessionUUID, disconnectedSessions[0])
	mu.Unlock()
}

func TestHub_GetClientCount(t *testing.T) {
	hub, dial := testHub(t, nil, nil)
	sessionUUID := uuid.New()

	assert.Equal(t, 0, hub.GetClientCount(sessionUUID))

	conn1 := dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 1))

	dial(sessionUUID)
	require.True(t, waitForClientCount(hub, sessionUUID, 2))

	conn1.Close()
	require.True(t, waitForClientCount(hub, sessionUUID, 1))
}

func TestHub_BroadcastNoClients(t *testing.T) {
	hub, _ := testHub(t, nil, nil)
	// Should not panic
	hub.Broadcast(uuid.New(), 50.0, "active")
}

func TestHub_MaxClientsPerSession(t *testing.T) {
	hub := NewHub(nil, nil)
	t.Cleanup(func() { hub.Stop() })

	sessionUUID := uuid.New()

	// Register maxClientsPerSession clients — all should succeed
	conns := make([]*ws.Conn, 0, maxClientsPerSession)
	for i := 0; i < maxClientsPerSession; i++ {
		server, client := newTestConnPair(t)
		errCh := make(chan error, 1)
		hub.cmdCh <- cmdRegister{sessionUUID: sessionUUID, conn: server, errCh: errCh}
		err := <-errCh
		require.NoError(t, err, "client %d should register successfully", i)
		conns = append(conns, client)
	}

	assert.Equal(t, maxClientsPerSession, hub.GetClientCount(sessionUUID))

	// The next client should be rejected
	server, client := newTestConnPair(t)
	errCh := make(chan error, 1)
	hub.cmdCh <- cmdRegister{sessionUUID: sessionUUID, conn: server, errCh: errCh}
	err := <-errCh
	assert.Error(t, err, "client beyond max should be rejected")
	assert.Contains(t, err.Error(), "max clients per session")

	// Clean up
	_ = client
	for _, c := range conns {
		c.Close()
	}
}

// newTestConnPair creates a connected pair of WebSocket connections for testing.
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

func TestHub_OnFirstConnectError(t *testing.T) {
	onFirst := func(id uuid.UUID) error {
		return fmt.Errorf("activation failed")
	}

	hub, dial := testHub(t, onFirst, nil)
	sessionUUID := uuid.New()

	conn := dial(sessionUUID)

	// The hub should close the connection when onFirstConnect fails
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := conn.ReadMessage()
	assert.Error(t, err, "connection should be closed after onFirstConnect error")

	// Session should not exist in hub
	assert.Equal(t, 0, hub.GetClientCount(sessionUUID))
}
