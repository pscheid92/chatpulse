package broadcast

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
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
}

func newClientWriter(connection *websocket.Conn, clock clockwork.Clock) *clientWriter {
	cw := &clientWriter{
		connection:  connection,
		clock:       clock,
		sendChannel: make(chan []byte, messageBufferSize),
		doneChannel: make(chan struct{}),
	}
	cw.configurePongHandler()
	go cw.run()
	return cw
}

func (cw *clientWriter) run() {
	ticker := cw.clock.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-cw.sendChannel:
			if !ok {
				return
			}
			cw.updateWriteDeadline()
			if err := cw.connection.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.Chan():
			cw.updateWriteDeadline()
			if err := cw.connection.WriteMessage(websocket.PingMessage, nil); err != nil {
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
