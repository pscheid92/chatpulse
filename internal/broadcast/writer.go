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
	messageBufferSize = 16
)

type clientWriter struct {
	connection  *websocket.Conn
	clock       clockwork.Clock
	sendChannel chan []byte
	doneChannel chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

func newClientWriter(connection *websocket.Conn, clock clockwork.Clock) *clientWriter {
	cw := &clientWriter{
		connection:  connection,
		clock:       clock,
		sendChannel: make(chan []byte, messageBufferSize),
		doneChannel: make(chan struct{}),
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

func (cw *clientWriter) configurePongHandler() {
	cw.updateReadDeadline()
	cw.connection.SetPongHandler(func(string) error {
		cw.updateReadDeadline()
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
