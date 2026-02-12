package broadcast

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientWriter_IdleTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping idle timeout test in short mode")
	}

	// Use fake clock for deterministic testing
	fakeClock := clockwork.NewFakeClock()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	cw := newClientWriter(server, fakeClock)
	t.Cleanup(func() { cw.stop() })

	// Initially not idle
	shouldDisconnect := cw.checkIdleTimeout()
	assert.False(t, shouldDisconnect)

	// Advance clock to idle warning threshold (4 minutes)
	fakeClock.Advance(idleWarningTime)

	// Should send warning but not disconnect
	shouldDisconnect = cw.checkIdleTimeout()
	assert.False(t, shouldDisconnect, "should not disconnect at warning threshold")

	cw.activityMutex.Lock()
	warningSent := cw.warningSent
	cw.activityMutex.Unlock()
	assert.True(t, warningSent, "warning should be sent")

	// Advance clock beyond idle timeout (5 minutes total)
	fakeClock.Advance(1*time.Minute + 10*time.Second)

	// Should signal disconnect
	shouldDisconnect = cw.checkIdleTimeout()
	assert.True(t, shouldDisconnect, "connection should be marked for disconnect due to idle timeout")
}

func TestClientWriter_ActivityResetsIdleTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping activity reset test in short mode")
	}

	fakeClock := clockwork.NewFakeClock()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	cw := newClientWriter(server, fakeClock)
	t.Cleanup(func() { cw.stop() })

	// Advance clock partway (3 minutes)
	fakeClock.Advance(3 * time.Minute)

	// Simulate pong response (activity)
	cw.recordActivity()

	// Advance another 3 minutes (total 6 minutes from start, but only 3 from activity)
	fakeClock.Advance(3 * time.Minute)

	// Client should still not timeout (activity reset the timer)
	shouldDisconnect := cw.checkIdleTimeout()
	assert.False(t, shouldDisconnect, "client should not timeout after activity reset")

	// Advance past timeout from the activity reset point
	fakeClock.Advance(3 * time.Minute) // Total 6 minutes from activity

	// Now should timeout
	shouldDisconnect = cw.checkIdleTimeout()
	assert.True(t, shouldDisconnect, "client should timeout 5 minutes after last activity")
}

func TestClientWriter_GracefulStop(t *testing.T) {
	engine := &mockEngine{value: 42.0}
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())

	sessionUUID := uuid.New()
	server, client := newTestConnPair(t)

	err := broadcaster.Register(sessionUUID, server)
	require.NoError(t, err)

	// Read initial broadcast to confirm connection
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = client.ReadMessage()
	require.NoError(t, err)

	// Stop broadcaster gracefully
	broadcaster.Stop()

	// Client should receive close frame with reason
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = client.ReadMessage()

	// WebSocket library returns CloseError when close frame is received
	if closeErr, ok := err.(*websocket.CloseError); ok {
		assert.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
		assert.Contains(t, closeErr.Text, "shutting down")
	} else {
		// Some implementations might just close the connection
		assert.Error(t, err, "connection should be closed")
	}
}

func TestClientWriter_StopIdempotent(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	t.Cleanup(func() { broadcaster.Stop() })

	sessionUUID := uuid.New()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	err := broadcaster.Register(sessionUUID, server)
	require.NoError(t, err)

	// Get the clientWriter
	clients := broadcaster.activeClients[sessionUUID]
	require.Len(t, clients, 1)
	var cw *clientWriter
	for _, writer := range clients {
		cw = writer
		break
	}
	require.NotNil(t, cw)

	// Call stop multiple times - should not panic
	cw.stop()
	cw.stop()
	cw.stop()
}

func TestClientWriter_ConcurrentStop(t *testing.T) {
	engine := &mockEngine{value: 10.0}
	broadcaster := NewBroadcaster(engine, nil, nil, clockwork.NewRealClock())
	t.Cleanup(func() { broadcaster.Stop() })

	sessionUUID := uuid.New()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	err := broadcaster.Register(sessionUUID, server)
	require.NoError(t, err)

	// Get the clientWriter
	clients := broadcaster.activeClients[sessionUUID]
	require.Len(t, clients, 1)
	var cw *clientWriter
	for _, writer := range clients {
		cw = writer
		break
	}
	require.NotNil(t, cw)

	// Call stop concurrently from multiple goroutines
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cw.stop()
		}()
	}

	// Should complete without panic or deadlock
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent stop calls deadlocked")
	}
}

func TestClientWriter_RecordActivity(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	cw := newClientWriter(server, fakeClock)
	t.Cleanup(func() { cw.stop() })

	// Check initial activity time
	cw.activityMutex.Lock()
	initialActivity := cw.lastActivity
	cw.activityMutex.Unlock()

	// Advance clock
	fakeClock.Advance(1 * time.Minute)

	// Record activity
	cw.recordActivity()

	// Check that lastActivity was updated
	cw.activityMutex.Lock()
	newActivity := cw.lastActivity
	cw.activityMutex.Unlock()

	assert.True(t, newActivity.After(initialActivity), "recordActivity should update lastActivity timestamp")
}

func TestClientWriter_CheckIdleTimeout_Warning(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	cw := newClientWriter(server, fakeClock)
	t.Cleanup(func() { cw.stop() })

	// Initially not idle
	shouldDisconnect := cw.checkIdleTimeout()
	assert.False(t, shouldDisconnect)

	// Advance to warning threshold
	fakeClock.Advance(idleWarningTime)

	// Should send warning but not disconnect
	shouldDisconnect = cw.checkIdleTimeout()
	assert.False(t, shouldDisconnect, "should not disconnect at warning threshold")

	// Warning flag should be set
	cw.activityMutex.Lock()
	warningSent := cw.warningSent
	cw.activityMutex.Unlock()
	assert.True(t, warningSent)
}

func TestClientWriter_CheckIdleTimeout_Disconnect(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	server, client := newTestConnPair(t)
	t.Cleanup(func() { client.Close() })

	cw := newClientWriter(server, fakeClock)
	t.Cleanup(func() { cw.stop() })

	// Advance beyond idle timeout
	fakeClock.Advance(idleTimeout + 1*time.Second)

	// Should signal disconnect
	shouldDisconnect := cw.checkIdleTimeout()
	assert.True(t, shouldDisconnect, "should disconnect after idle timeout")
}
