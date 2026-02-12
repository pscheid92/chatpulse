package broadcast

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

const (
	maxClientsPerSession = 50
	tickInterval         = 50 * time.Millisecond
	redisTimeout         = 2 * time.Second
	commandTimeout       = 5 * time.Second
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
	done           chan struct{}
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
		done:           make(chan struct{}),
	}
	go b.run()
	return b
}

// Register adds a client to a session. Non-blocking — just adds to the map.
// Returns error only if max clients per session is reached.
func (b *Broadcaster) Register(sessionUUID uuid.UUID, conn *websocket.Conn) error {
	errCh := make(chan error, 1)
	b.cmdCh <- registerCmd{sessionUUID: sessionUUID, connection: conn, errorChannel: errCh}

	// Use timeout to prevent blocking forever if broadcaster is stuck
	timer := b.clock.NewTimer(commandTimeout)
	defer timer.Stop()

	select {
	case err := <-errCh:
		return err
	case <-timer.Chan():
		return fmt.Errorf("register command timed out after %v", commandTimeout)
	}
}

// Unregister removes a client from a session.
func (b *Broadcaster) Unregister(sessionUUID uuid.UUID, conn *websocket.Conn) {
	b.cmdCh <- unregisterCmd{sessionUUID: sessionUUID, connection: conn}
}

// GetClientCount returns the number of connected clients for a session.
// Returns -1 if the command times out.
func (b *Broadcaster) GetClientCount(sessionUUID uuid.UUID) int {
	replyCh := make(chan int, 1)
	b.cmdCh <- getClientCountCmd{sessionUUID: sessionUUID, replyChannel: replyCh}

	// Use timeout to prevent blocking forever if broadcaster is stuck
	timer := b.clock.NewTimer(commandTimeout)
	defer timer.Stop()

	select {
	case count := <-replyCh:
		return count
	case <-timer.Chan():
		slog.Warn("GetClientCount timed out", "timeout", commandTimeout)
		return -1
	}
}

// Stop shuts down the broadcaster, closing all client connections.
// Blocks until the broadcaster goroutine has exited or timeout (10s) is reached.
func (b *Broadcaster) Stop() {
	b.cmdCh <- stopCmd{}

	// Wait for goroutine to exit with timeout
	timeout := b.clock.NewTimer(10 * time.Second)
	defer timeout.Stop()

	select {
	case <-b.done:
		// Clean shutdown
	case <-timeout.Chan():
		slog.Warn("Broadcaster Stop() timed out waiting for goroutine exit")
	}
}

func (b *Broadcaster) run() {
	ticker := b.clock.NewTicker(tickInterval)
	defer ticker.Stop()
	defer close(b.done)

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
				slog.Warn("Broadcaster received unknown command type", "command_type", fmt.Sprintf("%T", cmd))
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
		slog.Warn("Rejecting client: max clients reached", "session_uuid", c.sessionUUID.String(), "max_clients", maxClientsPerSession)
		c.connection.Close()
		c.errorChannel <- fmt.Errorf("max clients per session (%d) reached", maxClientsPerSession)
		return
	}

	if !exists && b.onFirstClient != nil {
		b.onFirstClient(c.sessionUUID)
	}

	cw := newClientWriter(c.connection, b.clock)
	clients[c.connection] = cw

	// Update metrics
	metrics.BroadcasterActiveSessions.Set(float64(len(b.activeClients)))
	metrics.BroadcasterConnectedClients.Inc()

	slog.Debug("Client registered", "session_uuid", c.sessionUUID.String(), "total_clients", len(clients))
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

	// Update metrics
	metrics.BroadcasterConnectedClients.Dec()

	if len(clients) == 0 {
		delete(b.activeClients, c.sessionUUID)
		metrics.BroadcasterActiveSessions.Set(float64(len(b.activeClients)))
		if b.onSessionEmpty != nil {
			b.onSessionEmpty(c.sessionUUID)
		}
		slog.Info("Last client disconnected", "session_uuid", c.sessionUUID.String())
	} else {
		slog.Debug("Client unregistered", "session_uuid", c.sessionUUID.String(), "remaining_clients", len(clients))
	}
}

func (b *Broadcaster) handleTick() {
	tickStart := b.clock.Now()

	for sessionUUID, clients := range b.activeClients {
		broadcastStart := b.clock.Now()

		// Per-session timeout to prevent Redis hangs from blocking the broadcaster
		ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
		value, err := b.engine.GetCurrentValue(ctx, sessionUUID)
		cancel()

		if err != nil {
			if err == context.DeadlineExceeded {
				slog.Warn("Redis timeout", "session_uuid", sessionUUID.String(), "timeout", redisTimeout)
			} else {
				slog.Error("GetCurrentValue error", "session_uuid", sessionUUID.String(), "error", err)
			}
			continue
		}

		update := domain.SessionUpdate{Value: value, Status: "active"}
		data, err := json.Marshal(update)
		if err != nil {
			slog.Error("Failed to marshal broadcast message", "error", err)
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
			slog.Warn("Disconnecting slow client", "session_uuid", sessionUUID.String())
			metrics.BroadcasterSlowClientsEvicted.Inc()
			cmd := unregisterCmd{sessionUUID: sessionUUID, connection: conn}
			b.handleUnregister(cmd)
		}

		// Track per-session broadcast duration
		broadcastDuration := b.clock.Since(broadcastStart).Seconds()
		metrics.BroadcasterBroadcastDuration.Observe(broadcastDuration)
	}

	// Track overall tick duration
	tickDuration := b.clock.Since(tickStart).Seconds()
	metrics.BroadcasterTickDuration.Observe(tickDuration)
}

func (b *Broadcaster) handleStop() {
	totalClients := 0
	for _, clients := range b.activeClients {
		totalClients += len(clients)
	}

	slog.Info("Broadcaster shutting down", "sessions", len(b.activeClients), "total_clients", totalClients)

	for sessionUUID, clients := range b.activeClients {
		for _, cw := range clients {
			cw.stopGraceful("Server shutting down")
		}
		delete(b.activeClients, sessionUUID)
		if b.onSessionEmpty != nil {
			b.onSessionEmpty(sessionUUID)
		}
	}

	slog.Info("Broadcaster shutdown complete", "disconnected_clients", totalClients)
}
