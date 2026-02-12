package broadcast

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

const (
	writeDeadline     = 5 * time.Second
	pingInterval      = 30 * time.Second
	pongDeadline      = 60 * time.Second
	idleTimeout       = 5 * time.Minute
	idleWarningTime   = 4 * time.Minute // Warn 1 minute before disconnect
	messageBufferSize = 16
)

type clientWriter struct {
	connection    *websocket.Conn
	clock         clockwork.Clock
	sendChannel   chan []byte
	doneChannel   chan struct{}
	stopOnce      sync.Once
	wg            sync.WaitGroup
	lastActivity  time.Time
	activityMutex sync.Mutex
	warningSent   bool
}

func newClientWriter(connection *websocket.Conn, clock clockwork.Clock) *clientWriter {
	now := clock.Now()
	cw := &clientWriter{
		connection:   connection,
		clock:        clock,
		sendChannel:  make(chan []byte, messageBufferSize),
		doneChannel:  make(chan struct{}),
		lastActivity: now,
	}
	cw.configurePongHandler()
	cw.wg.Add(1)
	go cw.run()
	return cw
}

func (cw *clientWriter) run() {
	ticker := cw.clock.NewTicker(pingInterval)
	defer ticker.Stop()
	defer cw.wg.Done()

	for {
		select {
		case msg, ok := <-cw.sendChannel:
			if !ok {
				return
			}
			// Track message send duration
			start := cw.clock.Now()
			cw.updateWriteDeadline()
			if err := cw.connection.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
			sendDuration := cw.clock.Since(start).Seconds()
			metrics.WebSocketMessageSendDuration.Observe(sendDuration)
		case <-ticker.Chan():
			// Check for idle timeout before sending ping
			if cw.checkIdleTimeout() {
				return
			}

			cw.updateWriteDeadline()
			if err := cw.connection.WriteMessage(websocket.PingMessage, nil); err != nil {
				// Ping failed - client likely disconnected
				metrics.WebSocketPingFailures.Inc()
				return
			}
		case <-cw.doneChannel:
			return
		}
	}
}

func (cw *clientWriter) stop() {
	cw.stopOnce.Do(func() {
		close(cw.doneChannel)
		_ = cw.connection.Close()
	})
	cw.wg.Wait()
}

// stopGraceful sends a WebSocket close frame with reason before closing.
func (cw *clientWriter) stopGraceful(reason string) {
	cw.stopOnce.Do(func() {
		// Signal the run goroutine to exit first
		close(cw.doneChannel)

		// Wait for run goroutine to exit before writing close frame
		// This prevents concurrent writes to the WebSocket connection
		cw.wg.Wait()

		// Now it's safe to write the close frame (run goroutine has exited)
		closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
		cw.updateWriteDeadline()
		_ = cw.connection.WriteMessage(websocket.CloseMessage, closeMsg)

		// Close the connection
		_ = cw.connection.Close()
	})
}

func (cw *clientWriter) configurePongHandler() {
	cw.updateReadDeadline()
	cw.connection.SetPongHandler(func(string) error {
		cw.updateReadDeadline()
		cw.recordActivity()
		return nil
	})
}

func (cw *clientWriter) updateWriteDeadline() {
	deadline := cw.clock.Now().Add(writeDeadline)
	_ = cw.connection.SetWriteDeadline(deadline)
}

func (cw *clientWriter) updateReadDeadline() {
	deadline := cw.clock.Now().Add(pongDeadline)
	_ = cw.connection.SetReadDeadline(deadline)
}

// recordActivity updates the last activity timestamp.
func (cw *clientWriter) recordActivity() {
	cw.activityMutex.Lock()
	defer cw.activityMutex.Unlock()
	cw.lastActivity = cw.clock.Now()
	cw.warningSent = false
}

// checkIdleTimeout checks if the connection is idle and sends a warning or disconnects.
// Returns true if the connection should be terminated.
func (cw *clientWriter) checkIdleTimeout() bool {
	cw.activityMutex.Lock()
	idleDuration := cw.clock.Since(cw.lastActivity)
	warningSent := cw.warningSent
	cw.activityMutex.Unlock()

	// Connection is idle beyond timeout - disconnect
	if idleDuration >= idleTimeout {
		metrics.WebSocketIdleDisconnects.Inc()
		return true
	}

	// Connection approaching idle timeout - send warning
	if !warningSent && idleDuration >= idleWarningTime {
		warning := []byte(`{"warning":"Connection idle. Will disconnect if no activity within 1 minute."}`)
		cw.updateWriteDeadline()
		if err := cw.connection.WriteMessage(websocket.TextMessage, warning); err == nil {
			cw.activityMutex.Lock()
			cw.warningSent = true
			cw.activityMutex.Unlock()
		}
	}

	return false
}
