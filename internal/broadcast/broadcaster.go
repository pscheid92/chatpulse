package broadcast

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const (
	maxClientsPerSession = 50
	tickInterval         = 50 * time.Millisecond
)

type sessionClients map[*websocket.Conn]*clientWriter

// broadcasterCmd is the command interface for the Broadcaster actor.
type broadcasterCmd interface{ isBroadcasterCmd() }

type baseBroadcasterCmd struct{}

func (baseBroadcasterCmd) isBroadcasterCmd() {}

type registerCmd struct {
	baseBroadcasterCmd
	sessionUUID  uuid.UUID
	connection   *websocket.Conn
	errorChannel chan error
}

type unregisterCmd struct {
	baseBroadcasterCmd
	sessionUUID uuid.UUID
	connection  *websocket.Conn
}

type getClientCountCmd struct {
	baseBroadcasterCmd
	sessionUUID  uuid.UUID
	replyChannel chan int
}

type stopCmd struct {
	baseBroadcasterCmd
}

// Broadcaster manages WebSocket connections and pulls sentiment values
// from the Engine on a tick loop, broadcasting to all connected clients.
type Broadcaster struct {
	cmdCh          chan broadcasterCmd
	clock          clockwork.Clock
	activeClients  map[uuid.UUID]sessionClients
	engine         domain.Engine
	onFirstClient  func(sessionUUID uuid.UUID)
	onSessionEmpty func(sessionUUID uuid.UUID)
}

// NewBroadcaster creates a new broadcaster.
// engine is used to pull current values on each tick.
// onFirstClient is called when the first client connects to a session on this instance.
// onSessionEmpty is called when the last client disconnects from a session.
func NewBroadcaster(engine domain.Engine, onFirstClient func(uuid.UUID), onSessionEmpty func(uuid.UUID), clock clockwork.Clock) *Broadcaster {
	b := &Broadcaster{
		cmdCh:          make(chan broadcasterCmd, 256),
		clock:          clock,
		activeClients:  make(map[uuid.UUID]sessionClients),
		engine:         engine,
		onFirstClient:  onFirstClient,
		onSessionEmpty: onSessionEmpty,
	}
	go b.run()
	return b
}

// Register adds a client to a session. Non-blocking — just adds to the map.
// Returns error only if max clients per session is reached.
func (b *Broadcaster) Register(sessionUUID uuid.UUID, conn *websocket.Conn) error {
	errCh := make(chan error, 1)
	b.cmdCh <- registerCmd{sessionUUID: sessionUUID, connection: conn, errorChannel: errCh}
	return <-errCh
}

// Unregister removes a client from a session.
func (b *Broadcaster) Unregister(sessionUUID uuid.UUID, conn *websocket.Conn) {
	b.cmdCh <- unregisterCmd{sessionUUID: sessionUUID, connection: conn}
}

// GetClientCount returns the number of connected clients for a session.
func (b *Broadcaster) GetClientCount(sessionUUID uuid.UUID) int {
	replyCh := make(chan int, 1)
	b.cmdCh <- getClientCountCmd{sessionUUID: sessionUUID, replyChannel: replyCh}
	return <-replyCh
}

// Stop shuts down the broadcaster, closing all client connections.
func (b *Broadcaster) Stop() {
	b.cmdCh <- stopCmd{}
}

func (b *Broadcaster) run() {
	ticker := b.clock.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case cmd := <-b.cmdCh:
			switch c := cmd.(type) {
			case registerCmd:
				b.handleRegister(c)
			case unregisterCmd:
				b.handleUnregister(c)
			case getClientCountCmd:
				c.replyChannel <- len(b.activeClients[c.sessionUUID])
			case stopCmd:
				b.handleStop()
				return
			default:
				log.Printf("Broadcaster: unknown command type %T", cmd)
			}
		case <-ticker.Chan():
			b.handleTick()
		}
	}
}

func (b *Broadcaster) handleRegister(c registerCmd) {
	clients, exists := b.activeClients[c.sessionUUID]
	if !exists {
		clients = make(sessionClients)
		b.activeClients[c.sessionUUID] = clients
	}

	if len(clients) >= maxClientsPerSession {
		log.Printf("Rejecting client for session %s: max clients (%d) reached", c.sessionUUID, maxClientsPerSession)
		c.connection.Close()
		c.errorChannel <- fmt.Errorf("max clients per session (%d) reached", maxClientsPerSession)
		return
	}

	if !exists && b.onFirstClient != nil {
		b.onFirstClient(c.sessionUUID)
	}

	cw := newClientWriter(c.connection, b.clock)
	clients[c.connection] = cw
	log.Printf("Client registered for session %s (total: %d)", c.sessionUUID, len(clients))
	c.errorChannel <- nil
}

func (b *Broadcaster) handleUnregister(c unregisterCmd) {
	clients, exists := b.activeClients[c.sessionUUID]
	if !exists {
		return
	}

	cw, exists := clients[c.connection]
	if !exists {
		return
	}

	cw.stop()
	delete(clients, c.connection)

	if len(clients) == 0 {
		delete(b.activeClients, c.sessionUUID)
		if b.onSessionEmpty != nil {
			b.onSessionEmpty(c.sessionUUID)
		}
		log.Printf("Last client disconnected for session %s", c.sessionUUID)
	} else {
		log.Printf("Client unregistered for session %s (remaining: %d)", c.sessionUUID, len(clients))
	}
}

func (b *Broadcaster) handleTick() {
	ctx := context.Background()
	for sessionUUID, clients := range b.activeClients {
		value, err := b.engine.GetCurrentValue(ctx, sessionUUID)
		if err != nil {
			log.Printf("GetCurrentValue error for session %s: %v", sessionUUID, err)
			continue
		}

		update := domain.SessionUpdate{Value: value, Status: "active"}
		data, err := json.Marshal(update)
		if err != nil {
			log.Printf("Failed to marshal broadcast message: %v", err)
			continue
		}

		var slow []*websocket.Conn
		for conn, writer := range clients {
			select {
			case writer.sendChannel <- data:
			default:
				slow = append(slow, conn)
			}
		}

		for _, conn := range slow {
			log.Printf("Disconnecting slow client for session %s", sessionUUID)
			cmd := unregisterCmd{sessionUUID: sessionUUID, connection: conn}
			b.handleUnregister(cmd)
		}
	}
}

func (b *Broadcaster) handleStop() {
	for sessionUUID, clients := range b.activeClients {
		for _, cw := range clients {
			cw.stop()
		}
		delete(b.activeClients, sessionUUID)
		if b.onSessionEmpty != nil {
			b.onSessionEmpty(sessionUUID)
		}
	}
}
