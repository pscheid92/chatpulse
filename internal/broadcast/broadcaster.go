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
	// Performance-critical constants (intentionally not configurable - see CLAUDE.md)
	redisTimeout                   = 2 * time.Second  // Coordinated with circuit breaker threshold
	commandTimeout                 = 5 * time.Second  // Actor command timeout
	maxTickDuration                = 40 * time.Millisecond // Leave 10ms margin from 50ms tick
	stopTimeout                    = 10 * time.Second // Graceful shutdown timeout
	circuitBreakerFailureThreshold = 5
	circuitBreakerOpenDuration     = 30 * time.Second
)

type sessionClients map[*websocket.Conn]*clientWriter

// sessionHealth tracks per-session circuit breaker state
type sessionHealth struct {
	consecutiveFailures int
	circuitOpen         bool
	openUntil           time.Time
}

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
	cmdCh                chan broadcasterCmd
	clock                clockwork.Clock
	activeClients        map[uuid.UUID]sessionClients
	sessionHealth        map[uuid.UUID]sessionHealth
	engine               domain.Engine
	onFirstClient        func(sessionUUID uuid.UUID)
	onSessionEmpty       func(sessionUUID uuid.UUID)
	done                 chan struct{}
	stopTimeout          time.Duration
	maxClientsPerSession int
	tickInterval         time.Duration
}

// NewBroadcaster creates a new broadcaster.
// engine is used to pull current values on each tick.
// onFirstClient is called when the first client connects to a session on this instance.
// onSessionEmpty is called when the last client disconnects from a session.
// maxClientsPerSession limits connections per session (prevents resource exhaustion).
// tickInterval controls broadcast frequency (lower = lower latency, higher Redis load).
func NewBroadcaster(engine domain.Engine, onFirstClient func(uuid.UUID), onSessionEmpty func(uuid.UUID), clock clockwork.Clock, maxClientsPerSession int, tickInterval time.Duration) *Broadcaster {
	b := &Broadcaster{
		cmdCh:                make(chan broadcasterCmd, 256),
		clock:                clock,
		activeClients:        make(map[uuid.UUID]sessionClients),
		sessionHealth:        make(map[uuid.UUID]sessionHealth),
		engine:               engine,
		onFirstClient:        onFirstClient,
		onSessionEmpty:       onSessionEmpty,
		done:                 make(chan struct{}),
		stopTimeout:          stopTimeout,
		maxClientsPerSession: maxClientsPerSession,
		tickInterval:         tickInterval,
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
// Blocks until the broadcaster goroutine has exited or timeout is reached.
func (b *Broadcaster) Stop() {
	b.cmdCh <- stopCmd{}

	// Wait for goroutine to exit with timeout
	timeout := b.clock.NewTimer(b.stopTimeout)
	defer timeout.Stop()

	select {
	case <-b.done:
		slog.Info("Broadcaster stopped gracefully")
	case <-timeout.Chan():
		slog.Warn("Broadcaster stop timeout exceeded, forcing exit",
			"timeout", b.stopTimeout,
		)
		metrics.BroadcasterStopTimeoutsTotal.Inc()

		// Force goroutine exit
		close(b.done)

		// Log goroutine leak for debugging
		slog.Error("Broadcaster goroutine may have leaked",
			"active_sessions", len(b.activeClients),
		)
	}
}

func (b *Broadcaster) run() {
	// Panic recovery wrapper
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Broadcaster panic recovered", "panic", r)
			metrics.BroadcasterPanicsTotal.Inc()

			// Attempt graceful cleanup
			b.closeAllClients("broadcaster panic")
		}
	}()

	ticker := b.clock.NewTicker(b.tickInterval)
	defer ticker.Stop()
	defer close(b.done)

	// Track command channel depth every second
	depthTicker := b.clock.NewTicker(1 * time.Second)
	defer depthTicker.Stop()

	for {
		select {
		case <-depthTicker.Chan():
			depth := len(b.cmdCh)
			metrics.BroadcasterCommandChannelDepth.Set(float64(depth))

			if depth > 200 { // 80% of 256
				slog.Warn("Command channel near capacity",
					"depth", depth,
					"capacity", cap(b.cmdCh),
				)
			}

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

	if len(clients) >= b.maxClientsPerSession {
		slog.Warn("Rejecting client: max clients reached", "session_uuid", c.sessionUUID.String(), "max_clients", b.maxClientsPerSession)
		c.connection.Close()
		c.errorChannel <- fmt.Errorf("max clients per session (%d) reached", b.maxClientsPerSession)
		return
	}

	// Run callback asynchronously to avoid blocking Register
	if !exists && b.onFirstClient != nil {
		go func() {
			b.onFirstClient(c.sessionUUID)
			// Note: Errors are handled by the callback itself (app.Service.IncrRefCount)
			// Worst case: ref count not incremented, session cleaned up early, client reconnects
		}()
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
		delete(b.sessionHealth, c.sessionUUID)
		metrics.BroadcasterActiveSessions.Set(float64(len(b.activeClients)))
		metrics.BroadcasterSessionCircuitState.DeleteLabelValues(c.sessionUUID.String())
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

	// Track tick duration and check budget
	defer func() {
		tickDuration := b.clock.Since(tickStart)
		metrics.BroadcasterTickDuration.Observe(tickDuration.Seconds())

		if tickDuration > maxTickDuration {
			slog.Warn("Tick duration exceeded budget",
				"duration", tickDuration,
				"budget", maxTickDuration,
				"sessions_processed", len(b.activeClients),
			)
			metrics.BroadcasterSlowTicksTotal.Inc()
		}
	}()

	for sessionUUID, clients := range b.activeClients {
		// Early abort if approaching budget
		if b.clock.Since(tickStart) > maxTickDuration {
			slog.Debug("Aborting tick early due to time budget",
				"elapsed", b.clock.Since(tickStart),
			)
			metrics.BroadcasterAbortedTicksTotal.Inc()
			break
		}

		broadcastStart := b.clock.Now()

		// Check circuit breaker state
		health := b.sessionHealth[sessionUUID]
		if health.circuitOpen && b.clock.Now().Before(health.openUntil) {
			// Circuit open, skip this session
			continue
		}

		// Per-session timeout to prevent Redis hangs from blocking the broadcaster
		ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
		value, err := b.engine.GetCurrentValue(ctx, sessionUUID)
		cancel()

		if err != nil {
			// Handle failure - increment circuit breaker counter
			health.consecutiveFailures++

			if health.consecutiveFailures >= circuitBreakerFailureThreshold {
				// Open circuit for 30 seconds
				health.circuitOpen = true
				health.openUntil = b.clock.Now().Add(circuitBreakerOpenDuration)

				slog.Warn("Session circuit opened due to failures",
					"session_uuid", sessionUUID.String(),
					"failures", health.consecutiveFailures,
				)
				metrics.BroadcasterSessionCircuitOpensTotal.Inc()
				metrics.BroadcasterSessionCircuitState.WithLabelValues(sessionUUID.String()).Set(1)
			}

			b.sessionHealth[sessionUUID] = health

			if err == context.DeadlineExceeded {
				slog.Warn("Redis timeout", "session_uuid", sessionUUID.String(), "timeout", redisTimeout)
			} else {
				slog.Error("GetCurrentValue error", "session_uuid", sessionUUID.String(), "error", err)
			}
			continue
		}

		// Success - reset circuit breaker
		if health.consecutiveFailures > 0 || health.circuitOpen {
			health.consecutiveFailures = 0
			health.circuitOpen = false
			b.sessionHealth[sessionUUID] = health
			metrics.BroadcasterSessionCircuitState.WithLabelValues(sessionUUID.String()).Set(0)
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
}

func (b *Broadcaster) handleStop() {
	totalClients := 0
	for _, clients := range b.activeClients {
		totalClients += len(clients)
	}

	slog.Info("Broadcaster shutting down", "sessions", len(b.activeClients), "total_clients", totalClients)

	b.closeAllClients("Server shutting down")

	slog.Info("Broadcaster shutdown complete", "disconnected_clients", totalClients)
}

// closeAllClients closes all client connections with the given reason.
// Used during panic recovery and graceful shutdown.
func (b *Broadcaster) closeAllClients(reason string) {
	for sessionUUID, clients := range b.activeClients {
		for _, cw := range clients {
			cw.stopGraceful(reason)
		}
		delete(b.activeClients, sessionUUID)
		delete(b.sessionHealth, sessionUUID)
		metrics.BroadcasterSessionCircuitState.DeleteLabelValues(sessionUUID.String())
		if b.onSessionEmpty != nil {
			b.onSessionEmpty(sessionUUID)
		}
	}
	metrics.BroadcasterActiveSessions.Set(0)
}
