package websocket

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const maxClientsPerSession = 50

// --- Command types ---

type hubCmd interface{ hubCmd() }

type cmdRegister struct {
	sessionUUID uuid.UUID
	conn        *websocket.Conn
	errCh       chan error
}

func (cmdRegister) hubCmd() {}

type cmdUnregister struct {
	sessionUUID uuid.UUID
	conn        *websocket.Conn
}

func (cmdUnregister) hubCmd() {}

type cmdBroadcast struct {
	sessionUUID uuid.UUID
	data        []byte
}

func (cmdBroadcast) hubCmd() {}

type cmdGetClientCount struct {
	sessionUUID uuid.UUID
	replyCh     chan int
}

func (cmdGetClientCount) hubCmd() {}

type cmdFirstConnectResult struct {
	sessionUUID uuid.UUID
	err         error
}

func (cmdFirstConnectResult) hubCmd() {}

type cmdStop struct{}

func (cmdStop) hubCmd() {}

// --- Per-connection writer ---

type clientWriter struct {
	conn   *websocket.Conn
	sendCh chan []byte
	done   chan struct{}
}

func newClientWriter(conn *websocket.Conn) *clientWriter {
	cw := &clientWriter{
		conn:   conn,
		sendCh: make(chan []byte, 16),
		done:   make(chan struct{}),
	}
	go cw.run()
	return cw
}

func (cw *clientWriter) run() {
	for {
		select {
		case msg, ok := <-cw.sendCh:
			if !ok {
				return
			}
			cw.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := cw.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-cw.done:
			return
		}
	}
}

func (cw *clientWriter) stop() {
	close(cw.done)
	cw.conn.Close()
}

// --- Hub ---

type Hub struct {
	cmdCh            chan hubCmd
	clients          map[uuid.UUID]map[*websocket.Conn]*clientWriter
	pendingClients   map[uuid.UUID][]cmdRegister
	onFirstConnect   func(uuid.UUID) error
	onLastDisconnect func(uuid.UUID)
}

func NewHub(onFirstConnect func(uuid.UUID) error, onLastDisconnect func(uuid.UUID)) *Hub {
	hub := &Hub{
		cmdCh:            make(chan hubCmd, 256),
		clients:          make(map[uuid.UUID]map[*websocket.Conn]*clientWriter),
		pendingClients:   make(map[uuid.UUID][]cmdRegister),
		onFirstConnect:   onFirstConnect,
		onLastDisconnect: onLastDisconnect,
	}
	go hub.run()
	return hub
}

func (h *Hub) run() {
	for cmd := range h.cmdCh {
		switch c := cmd.(type) {
		case cmdRegister:
			h.handleRegister(c)
		case cmdUnregister:
			h.handleUnregister(c.sessionUUID, c.conn)
		case cmdBroadcast:
			h.handleBroadcast(c)
		case cmdGetClientCount:
			clients := h.clients[c.sessionUUID]
			c.replyCh <- len(clients)
		case cmdFirstConnectResult:
			h.handleFirstConnectResult(c)
		case cmdStop:
			h.handleStop()
			return
		}
	}
}

func (h *Hub) handleRegister(c cmdRegister) {
	// Session already fully active — add client directly
	if clients, exists := h.clients[c.sessionUUID]; exists {
		if len(clients) >= maxClientsPerSession {
			log.Printf("Rejecting client for session %s: max clients (%d) reached", c.sessionUUID, maxClientsPerSession)
			c.conn.Close()
			c.errCh <- fmt.Errorf("max clients per session (%d) reached", maxClientsPerSession)
			return
		}
		cw := newClientWriter(c.conn)
		clients[c.conn] = cw
		log.Printf("Client registered for session %s (total clients: %d)", c.sessionUUID, len(clients))
		c.errCh <- nil
		return
	}

	// Session has a pending onFirstConnect — queue this client
	if _, exists := h.pendingClients[c.sessionUUID]; exists {
		h.pendingClients[c.sessionUUID] = append(h.pendingClients[c.sessionUUID], c)
		return
	}

	// New session — first client
	if h.onFirstConnect != nil {
		h.pendingClients[c.sessionUUID] = []cmdRegister{c}
		sessionUUID := c.sessionUUID
		go func() {
			err := h.onFirstConnect(sessionUUID)
			h.cmdCh <- cmdFirstConnectResult{
				sessionUUID: sessionUUID,
				err:         err,
			}
		}()
		return
	}

	// No onFirstConnect callback — register immediately
	clients := make(map[*websocket.Conn]*clientWriter)
	h.clients[c.sessionUUID] = clients
	cw := newClientWriter(c.conn)
	clients[c.conn] = cw
	log.Printf("Client registered for session %s (total clients: %d)", c.sessionUUID, len(clients))
	c.errCh <- nil
}

func (h *Hub) handleFirstConnectResult(c cmdFirstConnectResult) {
	pending, exists := h.pendingClients[c.sessionUUID]
	if !exists {
		return
	}
	delete(h.pendingClients, c.sessionUUID)

	if c.err != nil {
		log.Printf("Failed to activate session %s: %v", c.sessionUUID, c.err)
		for _, p := range pending {
			p.conn.Close()
			p.errCh <- c.err
		}
		return
	}

	clients := make(map[*websocket.Conn]*clientWriter)
	h.clients[c.sessionUUID] = clients
	for _, p := range pending {
		cw := newClientWriter(p.conn)
		clients[p.conn] = cw
		log.Printf("Client registered for session %s (total clients: %d)", c.sessionUUID, len(clients))
		p.errCh <- nil
	}
}

func (h *Hub) handleUnregister(sessionUUID uuid.UUID, conn *websocket.Conn) {
	clients, exists := h.clients[sessionUUID]
	if !exists {
		return
	}

	cw, exists := clients[conn]
	if !exists {
		return
	}

	cw.stop()
	delete(clients, conn)

	if len(clients) == 0 {
		delete(h.clients, sessionUUID)
		if h.onLastDisconnect != nil {
			h.onLastDisconnect(sessionUUID)
		}
		log.Printf("Last client disconnected for session %s", sessionUUID)
	} else {
		log.Printf("Client unregistered for session %s (remaining clients: %d)", sessionUUID, len(clients))
	}
}

func (h *Hub) handleBroadcast(c cmdBroadcast) {
	clients, exists := h.clients[c.sessionUUID]
	if !exists {
		return
	}

	var slow []*websocket.Conn
	for conn, cw := range clients {
		select {
		case cw.sendCh <- c.data:
			// sent successfully
		default:
			// client is slow, mark for removal
			slow = append(slow, conn)
		}
	}

	for _, conn := range slow {
		log.Printf("Disconnecting slow client for session %s", c.sessionUUID)
		h.handleUnregister(c.sessionUUID, conn)
	}
}

func (h *Hub) handleStop() {
	for sessionUUID, clients := range h.clients {
		for _, cw := range clients {
			cw.stop()
		}
		delete(h.clients, sessionUUID)
	}
	for sessionUUID, pending := range h.pendingClients {
		for _, p := range pending {
			p.conn.Close()
			p.errCh <- fmt.Errorf("hub stopped")
		}
		delete(h.pendingClients, sessionUUID)
	}
}

// --- Public API ---

func (h *Hub) Register(sessionUUID uuid.UUID, conn *websocket.Conn) error {
	errCh := make(chan error, 1)
	h.cmdCh <- cmdRegister{sessionUUID: sessionUUID, conn: conn, errCh: errCh}
	return <-errCh
}

func (h *Hub) Unregister(sessionUUID uuid.UUID, conn *websocket.Conn) {
	h.cmdCh <- cmdUnregister{sessionUUID: sessionUUID, conn: conn}
}

func (h *Hub) Broadcast(sessionUUID uuid.UUID, value float64, status string) {
	data, err := json.Marshal(map[string]interface{}{"value": value, "status": status})
	if err != nil {
		log.Printf("Failed to marshal broadcast message: %v", err)
		return
	}
	h.cmdCh <- cmdBroadcast{sessionUUID: sessionUUID, data: data}
}

func (h *Hub) GetClientCount(sessionUUID uuid.UUID) int {
	replyCh := make(chan int, 1)
	h.cmdCh <- cmdGetClientCount{sessionUUID: sessionUUID, replyCh: replyCh}
	return <-replyCh
}

func (h *Hub) Stop() {
	h.cmdCh <- cmdStop{}
}
