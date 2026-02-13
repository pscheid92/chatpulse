package broadcast

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/redis/go-redis/v9"
)

const (
	commandTimeout = 5 * time.Second  // Actor command timeout
	stopTimeout    = 10 * time.Second // Graceful shutdown timeout

	// Pub/sub reconnection backoff
	pubsubInitialBackoff = 1 * time.Second
	pubsubMaxBackoff     = 30 * time.Second

	// sentimentChannel is the Redis pub/sub channel that receives vote updates.
	// The apply_vote Lua function publishes JSON payloads to this channel.
	sentimentChannel = "sentiment:changes"
)

type sessionClients map[*websocket.Conn]*clientWriter

// sentimentMessage is the JSON payload published by the apply_vote Lua function.
type sentimentMessage struct {
	BroadcasterID string  `json:"broadcaster_id"`
	Value         float64 `json:"value"`
	Timestamp     int64   `json:"timestamp"`
}

// statusMessage is a lightweight message containing only a status field.
// Sent when the pub/sub subscriber transitions between connected and disconnected states.
type statusMessage struct {
	Status string `json:"status"`
}

// broadcasterCmd is the command interface for the Broadcaster actor.
type broadcasterCmd interface{ isBroadcasterCmd() }

type baseBroadcasterCmd struct{}

func (baseBroadcasterCmd) isBroadcasterCmd() {}

type subscribeCmd struct {
	baseBroadcasterCmd
	broadcasterID string
	connection    *websocket.Conn
	errorChannel  chan error
}

type unsubscribeCmd struct {
	baseBroadcasterCmd
	broadcasterID string
	connection    *websocket.Conn
}

type getClientCountCmd struct {
	baseBroadcasterCmd
	broadcasterID string
	replyChannel  chan int
}

type hasClientsCmd struct {
	baseBroadcasterCmd
	broadcasterID string
	replyChannel  chan bool
}

type sentimentUpdateCmd struct {
	baseBroadcasterCmd
	broadcasterID string
	value         float64
	timestamp     int64
	receivedAt    time.Time // When the pub/sub message was received (for latency tracking)
}

// statusChangeCmd is sent by the pub/sub subscriber to notify the actor
// about degraded/active state transitions.
type statusChangeCmd struct {
	baseBroadcasterCmd
	status string // "degraded" or "active"
}

type stopCmd struct {
	baseBroadcasterCmd
}

// execFuncCmd executes an arbitrary function within the actor goroutine.
// Used for testing to safely inspect or modify actor-owned state.
type execFuncCmd struct {
	baseBroadcasterCmd
	fn   func(*Broadcaster)
	done chan struct{}
}

// Broadcaster manages WebSocket connections and fans out sentiment updates
// received via Redis pub/sub to all connected WebSocket clients.
// Uses the actor pattern: single goroutine owns all mutable state.
type Broadcaster struct {
	cmdCh                    chan broadcasterCmd
	clock                    clockwork.Clock
	activeClients            map[string]sessionClients
	engine                   domain.Engine
	redisClient              *redis.Client
	done                     chan struct{}
	subscriberDone           chan struct{}
	stopTimeout              time.Duration
	maxClientsPerBroadcaster int
	tickInterval             time.Duration
	shutdownCtx              context.Context
	shutdownCancel           context.CancelFunc
}

// NewBroadcaster creates a new broadcaster.
// engine is retained for backward compatibility but no longer used for polling.
// redisClient is used to subscribe to sentiment update pub/sub messages.
// maxClientsPerBroadcaster limits connections per broadcaster (prevents resource exhaustion).
// tickInterval controls health-check frequency (ping/pong, idle detection).
func NewBroadcaster(engine domain.Engine, redisClient *redis.Client, clock clockwork.Clock, maxClientsPerBroadcaster int, tickInterval time.Duration) *Broadcaster {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	b := &Broadcaster{
		cmdCh:                    make(chan broadcasterCmd, 256),
		clock:                    clock,
		activeClients:            make(map[string]sessionClients),
		engine:                   engine,
		redisClient:              redisClient,
		done:                     make(chan struct{}),
		subscriberDone:           make(chan struct{}),
		stopTimeout:              stopTimeout,
		maxClientsPerBroadcaster: maxClientsPerBroadcaster,
		tickInterval:             tickInterval,
		shutdownCtx:              shutdownCtx,
		shutdownCancel:           shutdownCancel,
	}
	go b.run()
	if redisClient != nil {
		go b.subscribeSentiment()
	}
	return b
}

// Subscribe adds a client for a broadcaster. Non-blocking — just adds to the map.
// broadcasterID is the Twitch user ID of the broadcaster (used for Engine calls).
// Returns error only if max clients per broadcaster is reached.
func (b *Broadcaster) Subscribe(broadcasterID string, conn *websocket.Conn) error {
	errCh := make(chan error, 1)
	b.cmdCh <- subscribeCmd{broadcasterID: broadcasterID, connection: conn, errorChannel: errCh}

	// Use timeout to prevent blocking forever if broadcaster is stuck
	timer := b.clock.NewTimer(commandTimeout)
	defer timer.Stop()

	select {
	case err := <-errCh:
		return err
	case <-timer.Chan():
		return fmt.Errorf("subscribe command timed out after %v", commandTimeout)
	}
}

// Unsubscribe removes a client from a broadcaster.
func (b *Broadcaster) Unsubscribe(broadcasterID string, conn *websocket.Conn) {
	b.cmdCh <- unsubscribeCmd{broadcasterID: broadcasterID, connection: conn}
}

// GetClientCount returns the number of connected clients for a broadcaster.
// Returns -1 if the command times out.
func (b *Broadcaster) GetClientCount(broadcasterID string) int {
	replyCh := make(chan int, 1)
	b.cmdCh <- getClientCountCmd{broadcasterID: broadcasterID, replyChannel: replyCh}

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

// HasClients returns true if any client is connected for the given broadcaster ID.
// Returns true on timeout (fail-open: assume clients exist if broadcaster is busy).
func (b *Broadcaster) HasClients(broadcasterID string) bool {
	replyCh := make(chan bool, 1)
	b.cmdCh <- hasClientsCmd{broadcasterID: broadcasterID, replyChannel: replyCh}

	timer := b.clock.NewTimer(commandTimeout)
	defer timer.Stop()

	select {
	case has := <-replyCh:
		return has
	case <-timer.Chan():
		return true // fail-open
	}
}

// Stop shuts down the broadcaster, closing all client connections.
// Blocks until the broadcaster and subscriber goroutines have exited or timeout is reached.
func (b *Broadcaster) Stop() {
	// Cancel shutdown context to abort in-flight Redis operations and unblock subscriber
	b.shutdownCancel()

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
			"active_broadcasters", len(b.activeClients),
		)
	}

	// Wait for subscriber goroutine to exit (should be fast since context is cancelled)
	if b.redisClient != nil {
		select {
		case <-b.subscriberDone:
		case <-b.clock.After(5 * time.Second):
			slog.Warn("Subscriber goroutine did not exit in time")
		}
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
			case subscribeCmd:
				b.handleSubscribe(c)
			case unsubscribeCmd:
				b.handleUnsubscribe(c)
			case sentimentUpdateCmd:
				b.handleSentimentUpdate(c)
			case statusChangeCmd:
				b.handleStatusChange(c)
			case getClientCountCmd:
				c.replyChannel <- len(b.activeClients[c.broadcasterID])
			case hasClientsCmd:
				_, found := b.activeClients[c.broadcasterID]
				c.replyChannel <- found
			case stopCmd:
				b.handleStop()
				return
			case execFuncCmd:
				c.fn(b)
				if c.done != nil {
					close(c.done)
				}
			default:
				slog.Warn("Broadcaster received unknown command type", "command_type", fmt.Sprintf("%T", cmd))
			}
		case <-ticker.Chan():
			// Health-check tick: no-op in the actor loop.
			// Ping/pong and idle timeout detection are handled by each clientWriter.
		}
	}
}

func (b *Broadcaster) handleSubscribe(c subscribeCmd) {
	clients, exists := b.activeClients[c.broadcasterID]
	if !exists {
		clients = make(sessionClients)
		b.activeClients[c.broadcasterID] = clients
	}

	if len(clients) >= b.maxClientsPerBroadcaster {
		slog.Warn("Rejecting client: max clients reached", "broadcaster_id", c.broadcasterID, "max_clients", b.maxClientsPerBroadcaster)
		c.connection.Close()
		c.errorChannel <- fmt.Errorf("max clients per broadcaster (%d) reached", b.maxClientsPerBroadcaster)
		return
	}

	cw := newClientWriter(c.connection, b.clock)
	clients[c.connection] = cw

	// Update metrics
	metrics.BroadcasterActiveSessions.Set(float64(len(b.activeClients)))
	metrics.BroadcasterConnectedClients.Inc()

	slog.Debug("Client subscribed", "broadcaster_id", c.broadcasterID, "total_clients", len(clients))
	c.errorChannel <- nil
}

func (b *Broadcaster) handleUnsubscribe(c unsubscribeCmd) {
	clients, exists := b.activeClients[c.broadcasterID]
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
		delete(b.activeClients, c.broadcasterID)
		metrics.BroadcasterActiveSessions.Set(float64(len(b.activeClients)))
		slog.Info("Last client disconnected", "broadcaster_id", c.broadcasterID)
	} else {
		slog.Debug("Client unsubscribed", "broadcaster_id", c.broadcasterID, "remaining_clients", len(clients))
	}
}

// handleSentimentUpdate processes a sentiment update received via Redis pub/sub.
// It looks up the broadcaster's connected clients and fans out the update.
func (b *Broadcaster) handleSentimentUpdate(c sentimentUpdateCmd) {
	clients, exists := b.activeClients[c.broadcasterID]
	if !exists {
		return
	}

	// Track latency from pub/sub receive to fan-out
	latency := b.clock.Since(c.receivedAt).Seconds()
	metrics.PubSubMessageLatency.Observe(latency)

	// Look up config for decay speed via the engine
	ctx, cancel := context.WithTimeout(b.shutdownCtx, 2*time.Second)
	broadcastData, err := b.engine.GetBroadcastData(ctx, c.broadcasterID)
	cancel()

	var decaySpeed float64
	if err != nil || broadcastData == nil {
		// Fall back to a sensible default; the client will still display the value
		decaySpeed = 1.0
	} else {
		decaySpeed = broadcastData.DecaySpeed
	}

	update := domain.SessionUpdate{
		Value:      c.value,
		DecaySpeed: decaySpeed,
		Timestamp:  c.timestamp,
		Status:     "active",
	}
	data, err := json.Marshal(update)
	if err != nil {
		slog.Error("Failed to marshal sentiment update", "error", err)
		return
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
		slog.Warn("Disconnecting slow client", "broadcaster_id", c.broadcasterID)
		metrics.BroadcasterSlowClientsEvicted.Inc()
		cmd := unsubscribeCmd{broadcasterID: c.broadcasterID, connection: conn}
		b.handleUnsubscribe(cmd)
	}
}

// handleStatusChange broadcasts a status-only message to ALL connected clients.
// Triggered by the pub/sub subscriber on disconnect (degraded) or reconnect (active).
func (b *Broadcaster) handleStatusChange(c statusChangeCmd) {
	msg := statusMessage{Status: c.status}
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal status message", "error", err, "status", c.status)
		return
	}

	for broadcasterID, clients := range b.activeClients {
		for conn, writer := range clients {
			select {
			case writer.sendChannel <- data:
			default:
				slog.Warn("Dropping status message for slow client",
					"broadcaster_id", broadcasterID,
					"status", c.status,
				)
				_ = conn
			}
		}
	}
}

func (b *Broadcaster) handleStop() {
	totalClients := 0
	for _, clients := range b.activeClients {
		totalClients += len(clients)
	}

	slog.Info("Broadcaster shutting down", "broadcasters", len(b.activeClients), "total_clients", totalClients)

	b.closeAllClients("Server shutting down")

	slog.Info("Broadcaster shutdown complete", "disconnected_clients", totalClients)
}

// closeAllClients closes all client connections with the given reason.
// Used during panic recovery and graceful shutdown.
func (b *Broadcaster) closeAllClients(reason string) {
	for broadcasterID, clients := range b.activeClients {
		for _, cw := range clients {
			cw.stopGraceful(reason)
		}
		delete(b.activeClients, broadcasterID)
	}
	metrics.BroadcasterActiveSessions.Set(0)
}

// subscribeSentiment subscribes to the Redis pub/sub channel for sentiment updates
// and forwards them to the actor loop as sentimentUpdateCmd commands.
// Handles reconnection with exponential backoff on Redis disconnect.
func (b *Broadcaster) subscribeSentiment() {
	defer close(b.subscriberDone)

	backoff := pubsubInitialBackoff

	for {
		// Check if shutdown was requested
		if b.shutdownCtx.Err() != nil {
			slog.Info("Sentiment subscriber shutting down")
			return
		}

		err := b.runSubscription()
		if b.shutdownCtx.Err() != nil {
			// Context cancelled during subscription — clean shutdown
			return
		}

		// Subscription failed or disconnected — reconnect with backoff
		slog.Warn("Sentiment subscription disconnected, reconnecting",
			"error", err,
			"backoff", backoff,
		)
		metrics.PubSubReconnectionsTotal.Inc()
		metrics.PubSubSubscriptionActive.Set(0)

		// Notify clients of degraded state
		select {
		case b.cmdCh <- statusChangeCmd{status: "degraded"}:
		default:
			slog.Warn("Command channel full, could not send degraded status")
		}

		select {
		case <-b.clock.After(backoff):
			// Exponential backoff with cap
			backoff *= 2
			if backoff > pubsubMaxBackoff {
				backoff = pubsubMaxBackoff
			}
		case <-b.shutdownCtx.Done():
			return
		}
	}
}

// runSubscription subscribes to the sentiment:changes channel and processes messages
// until an error occurs or the context is cancelled.
func (b *Broadcaster) runSubscription() error {
	pubsub := b.redisClient.Subscribe(b.shutdownCtx, sentimentChannel)
	defer func() {
		_ = pubsub.Close()
	}()

	// Wait for confirmation of subscription
	_, err := pubsub.Receive(b.shutdownCtx)
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	slog.Info("Sentiment subscriber connected", "channel", sentimentChannel)
	metrics.PubSubSubscriptionActive.Set(1)

	// Notify clients that we're back to active
	select {
	case b.cmdCh <- statusChangeCmd{status: "active"}:
	default:
		slog.Warn("Command channel full, could not send active status")
	}

	ch := pubsub.Channel()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return fmt.Errorf("pub/sub channel closed")
			}

			receivedAt := b.clock.Now()
			metrics.PubSubMessagesReceived.WithLabelValues(sentimentChannel).Inc()

			var sentMsg sentimentMessage
			if err := json.Unmarshal([]byte(msg.Payload), &sentMsg); err != nil {
				slog.Warn("Failed to parse sentiment message",
					"payload", msg.Payload,
					"error", err,
				)
				continue
			}

			// Send to actor loop as a command (non-blocking with select)
			cmd := sentimentUpdateCmd{
				broadcasterID: sentMsg.BroadcasterID,
				value:         sentMsg.Value,
				timestamp:     sentMsg.Timestamp,
				receivedAt:    receivedAt,
			}
			select {
			case b.cmdCh <- cmd:
			default:
				slog.Warn("Command channel full, dropping sentiment update",
					"broadcaster_id", sentMsg.BroadcasterID,
				)
			}
		case <-b.shutdownCtx.Done():
			return nil
		}
	}
}
