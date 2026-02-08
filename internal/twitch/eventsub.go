package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// --- Connection states ---

type ConnectionState int

const (
	StateDisconnected ConnectionState = iota
	StateConnecting
	StateConnected
	StateReconnectingGraceful
	StateReconnectingUngraceful
)

const (
	maxReconnectBackoff = 60 * time.Second
	defaultEventSubURL  = "wss://eventsub.wss.twitch.tv/ws"
)

// --- Command types ---

type esubCmd interface{ esubCmd() }

type cmdSubscribe struct {
	userID            uuid.UUID
	broadcasterUserID string
	replyCh           chan error
}

func (cmdSubscribe) esubCmd() {}

type cmdUnsubscribe struct {
	userID  uuid.UUID
	replyCh chan error
}

func (cmdUnsubscribe) esubCmd() {}

type cmdIsReconnecting struct {
	replyCh chan bool
}

func (cmdIsReconnecting) esubCmd() {}

type cmdConnectResult struct {
	conn       *websocket.Conn
	url        string
	err        error
	generation uint64
}

func (cmdConnectResult) esubCmd() {}

type cmdWebSocketMessage struct {
	data       []byte
	generation uint64
}

func (cmdWebSocketMessage) esubCmd() {}

type cmdWebSocketError struct {
	err        error
	generation uint64
}

func (cmdWebSocketError) esubCmd() {}

type cmdKeepaliveTimeout struct {
	generation uint64
}

func (cmdKeepaliveTimeout) esubCmd() {}

type cmdReconnectAttempt struct {
	attempt    int
	generation uint64
}

func (cmdReconnectAttempt) esubCmd() {}

type cmdSubscribeResult struct {
	userID            uuid.UUID
	broadcasterUserID string
	subscriptionID    string
	err               error
	replyCh           chan error
}

func (cmdSubscribeResult) esubCmd() {}

type cmdUnsubscribeResult struct {
	userID  uuid.UUID
	err     error
	replyCh chan error
}

func (cmdUnsubscribeResult) esubCmd() {}

type cmdShutdown struct {
	doneCh chan struct{}
}

func (cmdShutdown) esubCmd() {}

// --- Pending subscribe request ---

type pendingSubscribe struct {
	userID            uuid.UUID
	broadcasterUserID string
	replyCh           chan error
}

// --- EventSubManager ---

type SentimentEngine interface {
	ProcessVote(broadcasterUserID, twitchUserID, messageText string)
}

type Subscription struct {
	UserID            uuid.UUID
	BroadcasterUserID string
	SubscriptionID    string
}

type EventSubManager struct {
	cmdCh           chan esubCmd
	helixClient     *HelixClient
	sentimentEngine SentimentEngine

	// Lock-free connection status for health checks and broadcast enrichment.
	// Written only by the actor goroutine, read from any goroutine.
	connectionStatus atomic.Value // stores string

	// Actor-owned state (only accessed in run goroutine)
	conn              *websocket.Conn
	connGeneration    uint64
	sessionID         string
	state             ConnectionState
	subscriptions     map[uuid.UUID]*Subscription
	keepaliveTimer    *time.Timer
	reconnecting      bool
	pendingSubscribes []pendingSubscribe
}

func NewEventSubManager(helixClient *HelixClient, sentimentEngine SentimentEngine) *EventSubManager {
	em := &EventSubManager{
		cmdCh:           make(chan esubCmd, 64),
		helixClient:     helixClient,
		sentimentEngine: sentimentEngine,
		subscriptions:   make(map[uuid.UUID]*Subscription),
		state:           StateDisconnected,
	}
	em.connectionStatus.Store("active")
	go em.run()
	return em
}

// GetConnectionStatus returns the current connection status string.
// Lock-free: safe to call from any goroutine (e.g., the engine ticker).
func (em *EventSubManager) GetConnectionStatus() string {
	if v := em.connectionStatus.Load(); v != nil {
		return v.(string)
	}
	return "active"
}

func (em *EventSubManager) run() {
	for cmd := range em.cmdCh {
		switch c := cmd.(type) {
		case cmdSubscribe:
			em.handleSubscribe(c)

		case cmdUnsubscribe:
			em.handleUnsubscribe(c)

		case cmdIsReconnecting:
			c.replyCh <- em.reconnecting

		case cmdConnectResult:
			em.handleConnectResult(c)

		case cmdWebSocketMessage:
			if c.generation != em.connGeneration {
				break // stale
			}
			em.handleMessage(c.data)

		case cmdWebSocketError:
			if c.generation != em.connGeneration {
				break // stale
			}
			log.Printf("EventSub read error: %v", c.err)
			em.handleDisconnect()

		case cmdKeepaliveTimeout:
			if c.generation != em.connGeneration {
				break // stale
			}
			log.Println("Keepalive timeout, connection appears dead")
			em.handleDisconnect()

		case cmdReconnectAttempt:
			if c.generation != em.connGeneration {
				break // stale generation, a newer connect succeeded
			}
			em.startConnect(defaultEventSubURL)

		case cmdSubscribeResult:
			em.handleSubscribeResult(c)

		case cmdUnsubscribeResult:
			c.replyCh <- c.err

		case cmdShutdown:
			em.handleShutdown(c)
			return
		}
	}
}

// --- Actor handlers ---

func (em *EventSubManager) handleSubscribe(c cmdSubscribe) {
	if _, exists := em.subscriptions[c.userID]; exists {
		c.replyCh <- nil
		return
	}

	if em.state != StateConnected {
		// Queue and ensure we're connecting
		em.pendingSubscribes = append(em.pendingSubscribes, pendingSubscribe{
			userID:            c.userID,
			broadcasterUserID: c.broadcasterUserID,
			replyCh:           c.replyCh,
		})
		if em.state == StateDisconnected {
			em.startConnect(defaultEventSubURL)
		}
		return
	}

	// Connected: fire off the API call in a background goroutine
	sessionID := em.sessionID
	go func() {
		subID, err := em.helixClient.CreateEventSubSubscription(
			context.Background(), c.userID, "channel.chat.message", c.broadcasterUserID, sessionID,
		)
		em.cmdCh <- cmdSubscribeResult{
			userID:            c.userID,
			broadcasterUserID: c.broadcasterUserID,
			subscriptionID:    subID,
			err:               err,
			replyCh:           c.replyCh,
		}
	}()
}

func (em *EventSubManager) handleSubscribeResult(c cmdSubscribeResult) {
	if c.err != nil {
		log.Printf("Failed to subscribe for user %s: %v", c.userID, c.err)
		c.replyCh <- c.err
		return
	}

	em.subscriptions[c.userID] = &Subscription{
		UserID:            c.userID,
		BroadcasterUserID: c.broadcasterUserID,
		SubscriptionID:    c.subscriptionID,
	}
	log.Printf("Subscribed to chat messages for broadcaster %s (subscription ID: %s)", c.broadcasterUserID, c.subscriptionID)
	c.replyCh <- nil
}

func (em *EventSubManager) handleUnsubscribe(c cmdUnsubscribe) {
	sub, exists := em.subscriptions[c.userID]
	if !exists {
		c.replyCh <- nil
		return
	}
	delete(em.subscriptions, c.userID)

	if em.reconnecting {
		c.replyCh <- nil
		return
	}

	subID := sub.SubscriptionID
	go func() {
		err := em.helixClient.DeleteEventSubSubscription(context.Background(), c.userID, subID)
		if err != nil {
			log.Printf("Failed to delete subscription %s: %v", subID, err)
		} else {
			log.Printf("Unsubscribed from chat messages for broadcaster %s", sub.BroadcasterUserID)
		}
		em.cmdCh <- cmdUnsubscribeResult{userID: c.userID, err: err, replyCh: c.replyCh}
	}()
}

func (em *EventSubManager) startConnect(url string) {
	em.connGeneration++
	gen := em.connGeneration
	em.state = StateConnecting
	em.connectionStatus.Store("connecting")

	go func() {
		// Create timeout context for dial (15 seconds)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Use dialer with explicit handshake timeout
		dialer := websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		}

		conn, _, err := dialer.DialContext(ctx, url, nil)
		em.cmdCh <- cmdConnectResult{conn: conn, url: url, err: err, generation: gen}
	}()
}

func (em *EventSubManager) handleConnectResult(c cmdConnectResult) {
	if c.generation != em.connGeneration {
		// Stale connect result; close the connection if it succeeded
		if c.conn != nil {
			c.conn.Close()
		}
		return
	}

	if c.err != nil {
		log.Printf("Failed to connect to eventsub: %v", c.err)
		// Schedule reconnect if we have subscriptions
		if len(em.subscriptions) > 0 || len(em.pendingSubscribes) > 0 {
			em.scheduleReconnect(0)
		} else {
			em.state = StateDisconnected
			em.connectionStatus.Store("active")
			// Fail any pending subscribes
			em.failPendingSubscribes(fmt.Errorf("failed to connect to eventsub: %w", c.err))
		}
		return
	}

	// Close old connection if any
	if em.conn != nil {
		em.conn.Close()
	}
	em.conn = c.conn

	// Start read pump
	gen := em.connGeneration
	go em.readPump(c.conn, gen)

	log.Printf("EventSub WebSocket connected to %s", c.url)
}

func (em *EventSubManager) readPump(conn *websocket.Conn, generation uint64) {
	defer conn.Close()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			em.cmdCh <- cmdWebSocketError{err: err, generation: generation}
			return
		}
		em.cmdCh <- cmdWebSocketMessage{data: data, generation: generation}
	}
}

func (em *EventSubManager) handleMessage(data []byte) {
	var wrapper struct {
		Metadata struct {
			MessageType string `json:"message_type"`
		} `json:"metadata"`
		Payload json.RawMessage `json:"payload"`
	}

	if err := json.Unmarshal(data, &wrapper); err != nil {
		log.Printf("EventSub message parse error: %v", err)
		return
	}

	switch wrapper.Metadata.MessageType {
	case "session_welcome":
		em.handleWelcome(wrapper.Payload)
	case "session_keepalive":
		em.resetKeepaliveTimer(0)
	case "notification":
		em.handleNotification(wrapper.Payload)
	case "session_reconnect":
		em.handleReconnect(wrapper.Payload)
	}
}

func (em *EventSubManager) handleWelcome(payload json.RawMessage) {
	var welcome struct {
		Session struct {
			ID                      string `json:"id"`
			KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
		} `json:"session"`
	}

	if err := json.Unmarshal(payload, &welcome); err != nil {
		log.Printf("EventSub welcome parse error: %v", err)
		return
	}

	wasReconnecting := em.reconnecting
	em.sessionID = welcome.Session.ID
	em.reconnecting = false
	em.state = StateConnected
	em.connectionStatus.Store("active")

	log.Printf("EventSub connected with session ID: %s", welcome.Session.ID)

	// Clamp keepalive timeout to sane range
	keepaliveSecs := welcome.Session.KeepaliveTimeoutSeconds
	if keepaliveSecs < 10 {
		keepaliveSecs = 10
	} else if keepaliveSecs > 600 {
		keepaliveSecs = 600
	}
	timeout := time.Duration(keepaliveSecs+5) * time.Second
	em.resetKeepaliveTimer(timeout)

	// Process pending subscribes
	em.processPendingSubscribes()

	// If this was an ungraceful reconnect, re-subscribe existing subscriptions
	if wasReconnecting {
		em.resubscribeAll()
	}
}

func (em *EventSubManager) handleReconnect(payload json.RawMessage) {
	var reconnect struct {
		Session struct {
			ReconnectURL string `json:"reconnect_url"`
		} `json:"session"`
	}

	if err := json.Unmarshal(payload, &reconnect); err != nil {
		log.Printf("EventSub reconnect parse error: %v", err)
		return
	}

	log.Printf("EventSub graceful reconnect to: %s", reconnect.Session.ReconnectURL)
	em.state = StateReconnectingGraceful
	em.connectionStatus.Store("connecting")
	em.startConnect(reconnect.Session.ReconnectURL)
}

func (em *EventSubManager) handleNotification(payload json.RawMessage) {
	var notification struct {
		Subscription struct {
			Type string `json:"type"`
		} `json:"subscription"`
		Event json.RawMessage `json:"event"`
	}

	if err := json.Unmarshal(payload, &notification); err != nil {
		log.Printf("EventSub notification parse error: %v", err)
		return
	}

	if notification.Subscription.Type == "channel.chat.message" {
		em.handleChatMessage(notification.Event)
	}
}

func (em *EventSubManager) handleChatMessage(event json.RawMessage) {
	var chatEvent struct {
		BroadcasterUserID string `json:"broadcaster_user_id"`
		ChatterUserID     string `json:"chatter_user_id"`
		Message           struct {
			Text string `json:"text"`
		} `json:"message"`
	}

	if err := json.Unmarshal(event, &chatEvent); err != nil {
		log.Printf("EventSub chat message parse error: %v", err)
		return
	}

	em.sentimentEngine.ProcessVote(
		chatEvent.BroadcasterUserID,
		chatEvent.ChatterUserID,
		chatEvent.Message.Text,
	)
}

func (em *EventSubManager) handleDisconnect() {
	hasSubscriptions := len(em.subscriptions) > 0
	wasGraceful := em.state == StateReconnectingGraceful

	em.state = StateReconnectingUngraceful
	em.connectionStatus.Store("connecting")

	if !hasSubscriptions && len(em.pendingSubscribes) == 0 {
		em.state = StateDisconnected
		em.connectionStatus.Store("active")
		log.Println("EventSub disconnected (no active subscriptions, will reconnect on demand)")
		return
	}

	em.reconnecting = true

	if wasGraceful {
		log.Println("Graceful reconnect failed, falling back to ungraceful reconnect")
	} else {
		log.Println("Connection lost, initiating ungraceful reconnect")
	}

	em.scheduleReconnect(0)
}

func (em *EventSubManager) scheduleReconnect(attempt int) {
	backoff := time.Duration(1<<uint(attempt)) * time.Second
	if backoff > maxReconnectBackoff {
		backoff = maxReconnectBackoff
	}

	gen := em.connGeneration
	log.Printf("Reconnecting in %v (attempt %d)...", backoff, attempt+1)

	time.AfterFunc(backoff, func() {
		em.cmdCh <- cmdReconnectAttempt{attempt: attempt + 1, generation: gen}
	})
}

func (em *EventSubManager) resetKeepaliveTimer(duration time.Duration) {
	if em.keepaliveTimer != nil {
		em.keepaliveTimer.Stop()
	}

	if duration == 0 {
		duration = 20 * time.Second
	}

	gen := em.connGeneration
	em.keepaliveTimer = time.AfterFunc(duration, func() {
		em.cmdCh <- cmdKeepaliveTimeout{generation: gen}
	})
}

func (em *EventSubManager) processPendingSubscribes() {
	pending := em.pendingSubscribes
	em.pendingSubscribes = nil

	sessionID := em.sessionID
	for _, p := range pending {
		userID := p.userID
		broadcasterUserID := p.broadcasterUserID
		replyCh := p.replyCh
		go func() {
			subID, err := em.helixClient.CreateEventSubSubscription(
				context.Background(), userID, "channel.chat.message", broadcasterUserID, sessionID,
			)
			em.cmdCh <- cmdSubscribeResult{
				userID:            userID,
				broadcasterUserID: broadcasterUserID,
				subscriptionID:    subID,
				err:               err,
				replyCh:           replyCh,
			}
		}()
	}
}

func (em *EventSubManager) resubscribeAll() {
	// Copy current subscriptions and clear old IDs
	subs := make([]*Subscription, 0, len(em.subscriptions))
	for _, sub := range em.subscriptions {
		subs = append(subs, sub)
	}
	em.subscriptions = make(map[uuid.UUID]*Subscription)

	log.Printf("Re-subscribing to %d subscriptions after ungraceful reconnect", len(subs))

	sessionID := em.sessionID
	for i, sub := range subs {
		userID := sub.UserID
		broadcasterUserID := sub.BroadcasterUserID
		delay := time.Duration(i) * 100 * time.Millisecond

		// Stagger re-subscriptions to avoid rate limits
		time.AfterFunc(delay, func() {
			subID, err := em.helixClient.CreateEventSubSubscription(
				context.Background(), userID, "channel.chat.message", broadcasterUserID, sessionID,
			)
			// Use a no-op reply channel since this is an internal re-subscribe
			noopCh := make(chan error, 1)
			em.cmdCh <- cmdSubscribeResult{
				userID:            userID,
				broadcasterUserID: broadcasterUserID,
				subscriptionID:    subID,
				err:               err,
				replyCh:           noopCh,
			}
		})
	}
}

func (em *EventSubManager) failPendingSubscribes(err error) {
	for _, p := range em.pendingSubscribes {
		p.replyCh <- err
	}
	em.pendingSubscribes = nil
}

func (em *EventSubManager) handleShutdown(c cmdShutdown) {
	if em.keepaliveTimer != nil {
		em.keepaliveTimer.Stop()
	}
	if em.conn != nil {
		em.conn.Close()
	}
	close(c.doneCh)
}

// --- Public API ---

func (em *EventSubManager) Subscribe(userID uuid.UUID, broadcasterUserID string) error {
	replyCh := make(chan error, 1)
	em.cmdCh <- cmdSubscribe{
		userID:            userID,
		broadcasterUserID: broadcasterUserID,
		replyCh:           replyCh,
	}
	return <-replyCh
}

func (em *EventSubManager) Unsubscribe(userID uuid.UUID) error {
	replyCh := make(chan error, 1)
	em.cmdCh <- cmdUnsubscribe{userID: userID, replyCh: replyCh}
	return <-replyCh
}

func (em *EventSubManager) IsReconnecting() bool {
	replyCh := make(chan bool, 1)
	em.cmdCh <- cmdIsReconnecting{replyCh: replyCh}
	return <-replyCh
}

func (em *EventSubManager) Shutdown() {
	doneCh := make(chan struct{})
	em.cmdCh <- cmdShutdown{doneCh: doneCh}
	<-doneCh
}
