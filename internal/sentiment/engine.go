package sentiment

import (
	"context"
	"log"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/twitch-tow/internal/models"
)

// ConfigStore is the subset of database operations needed by the engine.
type ConfigStore interface {
	GetConfig(ctx context.Context, userID uuid.UUID) (*models.Config, error)
}

type Broadcaster interface {
	Broadcast(sessionUUID uuid.UUID, value float64, status string)
}

type TwitchManager interface {
	Unsubscribe(userID uuid.UUID) error
	IsReconnecting() bool
}

// --- Command types ---

type engineCmd interface{ engineCmd() }

type cmdCheckSession struct {
	sessionUUID uuid.UUID
	replyCh     chan bool
}

func (cmdCheckSession) engineCmd() {}

type cmdResumeSession struct {
	sessionUUID uuid.UUID
	replyCh     chan error
}

func (cmdResumeSession) engineCmd() {}

type cmdActivateSession struct {
	sessionUUID       uuid.UUID
	broadcasterUserID string
	config            models.ConfigSnapshot
	replyCh           chan error
}

func (cmdActivateSession) engineCmd() {}

type cmdProcessVote struct {
	broadcasterUserID string
	twitchUserID      string
	messageText       string
}

func (cmdProcessVote) engineCmd() {}

type cmdTick struct{}

func (cmdTick) engineCmd() {}

type cmdResetSession struct {
	sessionUUID uuid.UUID
}

func (cmdResetSession) engineCmd() {}

type cmdMarkDisconnected struct {
	sessionUUID uuid.UUID
}

func (cmdMarkDisconnected) engineCmd() {}

type cmdCleanupOrphans struct {
	twitchManager TwitchManager
}

func (cmdCleanupOrphans) engineCmd() {}

type cmdUpdateSessionConfig struct {
	sessionUUID uuid.UUID
	config      models.ConfigSnapshot
}

func (cmdUpdateSessionConfig) engineCmd() {}

type cmdGetSessionConfig struct {
	sessionUUID uuid.UUID
	replyCh     chan *models.ConfigSnapshot
}

func (cmdGetSessionConfig) engineCmd() {}

type cmdGetSessionValue struct {
	sessionUUID uuid.UUID
	replyCh     chan sessionValueResult
}

func (cmdGetSessionValue) engineCmd() {}

type sessionValueResult struct {
	value float64
	ok    bool
}

type cmdSetBroadcaster struct {
	b Broadcaster
}

func (cmdSetBroadcaster) engineCmd() {}

type cmdStop struct {
	doneCh chan struct{}
}

func (cmdStop) engineCmd() {}

// --- Engine ---

type Session struct {
	Value                float64
	LastClientDisconnect time.Time
	Config               models.ConfigSnapshot
}

type debounceKey struct {
	SessionUUID  uuid.UUID
	TwitchUserID string
}

type Engine struct {
	cmdCh                chan engineCmd
	db                   ConfigStore
	clock                clockwork.Clock
	activeSessions       map[uuid.UUID]*Session
	broadcasterToSession map[string]uuid.UUID
	debounceMap          map[debounceKey]time.Time
	broadcaster          Broadcaster
	statusFn             func() string
	stopCh               chan struct{}
}

func NewEngine(db ConfigStore, broadcaster Broadcaster) *Engine {
	return &Engine{
		cmdCh:                make(chan engineCmd, 512),
		db:                   db,
		clock:                clockwork.NewRealClock(),
		activeSessions:       make(map[uuid.UUID]*Session),
		broadcasterToSession: make(map[string]uuid.UUID),
		debounceMap:          make(map[debounceKey]time.Time),
		broadcaster:          broadcaster,
		stopCh:               make(chan struct{}),
	}
}

// SetStatusFunc sets a callback that returns the current connection status
// (e.g., "active", "connecting"). Called from the ticker goroutine, so it
// must not block. Typically wired to EventSubManager.GetConnectionStatus.
func (e *Engine) SetStatusFunc(fn func() string) {
	e.statusFn = fn
}

// SetBroadcaster sets the broadcaster for the engine. Used to resolve circular
// dependency where Engine needs Hub for broadcasting but Hub needs Engine for
// callbacks. Must be called before Start().
func (e *Engine) SetBroadcaster(b Broadcaster) {
	e.cmdCh <- cmdSetBroadcaster{b: b}
}

// Start begins the engine's background goroutines (ticker and actor).
// IMPORTANT: Call SetBroadcaster() before Start() to ensure broadcasts work correctly.
// The broadcaster is set asynchronously, so it should be called with enough time for
// the command to be processed before the first broadcast occurs.
func (e *Engine) Start() {
	go e.tickerLoop()
	go e.run()
}

func (e *Engine) run() {
	tickCount := 0
	for cmd := range e.cmdCh {
		switch c := cmd.(type) {
		case cmdSetBroadcaster:
			e.broadcaster = c.b
			log.Printf("Broadcaster set for engine")

		case cmdCheckSession:
			_, exists := e.activeSessions[c.sessionUUID]
			c.replyCh <- exists

		case cmdResumeSession:
			if session, exists := e.activeSessions[c.sessionUUID]; exists {
				session.LastClientDisconnect = time.Time{}
				log.Printf("Session %s already active, resuming", c.sessionUUID)
			}
			c.replyCh <- nil

		case cmdActivateSession:
			// Idempotent: if already exists (TOCTOU between check and activate), just resume
			if session, exists := e.activeSessions[c.sessionUUID]; exists {
				session.LastClientDisconnect = time.Time{}
				c.replyCh <- nil
				break
			}
			e.activeSessions[c.sessionUUID] = &Session{
				Value:  0,
				Config: c.config,
			}
			e.broadcasterToSession[c.broadcasterUserID] = c.sessionUUID
			log.Printf("Session %s activated (broadcaster %s)", c.sessionUUID, c.broadcasterUserID)
			c.replyCh <- nil

		case cmdProcessVote:
			e.handleProcessVote(c)

		case cmdTick:
			e.handleTick()
			tickCount++
			if tickCount%1000 == 0 {
				e.pruneDebounceMap()
			}

		case cmdResetSession:
			if session, exists := e.activeSessions[c.sessionUUID]; exists {
				session.Value = 0
				log.Printf("Session %s reset", c.sessionUUID)
			}

		case cmdMarkDisconnected:
			if session, exists := e.activeSessions[c.sessionUUID]; exists {
				session.LastClientDisconnect = e.clock.Now()
				log.Printf("Session %s marked as disconnected", c.sessionUUID)
			}

		case cmdCleanupOrphans:
			e.handleCleanupOrphans(c.twitchManager)

		case cmdUpdateSessionConfig:
			if session, exists := e.activeSessions[c.sessionUUID]; exists {
				session.Config = c.config
				log.Printf("Session %s config updated", c.sessionUUID)
			}

		case cmdGetSessionConfig:
			session, exists := e.activeSessions[c.sessionUUID]
			if !exists {
				c.replyCh <- nil
			} else {
				snapshot := session.Config // copy
				c.replyCh <- &snapshot
			}

		case cmdGetSessionValue:
			session, exists := e.activeSessions[c.sessionUUID]
			if !exists {
				c.replyCh <- sessionValueResult{ok: false}
			} else {
				c.replyCh <- sessionValueResult{value: session.Value, ok: true}
			}

		case cmdStop:
			close(e.stopCh)
			close(c.doneCh)
			return
		}
	}
}

func (e *Engine) handleProcessVote(c cmdProcessVote) {
	sessionUUID, exists := e.broadcasterToSession[c.broadcasterUserID]
	if !exists {
		return
	}

	targetSession, exists := e.activeSessions[sessionUUID]
	if !exists {
		return
	}

	lowerText := strings.ToLower(c.messageText)
	lowerFor := strings.ToLower(targetSession.Config.ForTrigger)
	lowerAgainst := strings.ToLower(targetSession.Config.AgainstTrigger)

	var delta float64
	if strings.Contains(lowerText, lowerFor) {
		delta = 10
	} else if strings.Contains(lowerText, lowerAgainst) {
		delta = -10
	} else {
		return
	}

	key := debounceKey{SessionUUID: sessionUUID, TwitchUserID: c.twitchUserID}
	lastVote, exists := e.debounceMap[key]
	if exists && e.clock.Since(lastVote) < 1*time.Second {
		return
	}

	targetSession.Value = clamp(targetSession.Value+delta, -100, 100)
	e.debounceMap[key] = e.clock.Now()

	log.Printf("Vote processed: user=%s, delta=%.0f, new value=%.2f", c.twitchUserID, delta, targetSession.Value)
}

func (e *Engine) handleTick() {
	status := "active"
	if e.statusFn != nil {
		status = e.statusFn()
	}

	updates := make(map[uuid.UUID]float64)
	for id, session := range e.activeSessions {
		decayFactor := math.Exp(-session.Config.DecaySpeed * 0.05)
		session.Value *= decayFactor
		updates[id] = session.Value
	}

	for id, value := range updates {
		e.broadcaster.Broadcast(id, value, status)
	}
}

func (e *Engine) handleCleanupOrphans(twitchManager TwitchManager) {
	orphans := make([]uuid.UUID, 0)
	now := e.clock.Now()

	for id, session := range e.activeSessions {
		if !session.LastClientDisconnect.IsZero() && now.Sub(session.LastClientDisconnect) > 30*time.Second {
			orphans = append(orphans, id)
		}
	}

	for _, id := range orphans {
		for broadcasterID, sessionUUID := range e.broadcasterToSession {
			if sessionUUID == id {
				delete(e.broadcasterToSession, broadcasterID)
				break
			}
		}
		delete(e.activeSessions, id)
		for key := range e.debounceMap {
			if key.SessionUUID == id {
				delete(e.debounceMap, key)
			}
		}
	}

	// Unsubscribe outside actor via goroutine
	if !twitchManager.IsReconnecting() {
		go func() {
			for _, id := range orphans {
				if err := twitchManager.Unsubscribe(id); err != nil {
					log.Printf("Failed to unsubscribe orphan session %s: %v", id, err)
				}
				log.Printf("Cleaned up orphan session %s", id)
			}
		}()
	}
}

func (e *Engine) pruneDebounceMap() {
	now := e.clock.Now()
	for key, lastVote := range e.debounceMap {
		if now.Sub(lastVote) > 5*time.Second {
			delete(e.debounceMap, key)
		}
	}
}

func (e *Engine) tickerLoop() {
	ticker := e.clock.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.Chan():
			e.cmdCh <- cmdTick{}
		case <-e.stopCh:
			return
		}
	}
}

// --- Public API ---

func (e *Engine) ActivateSession(ctx context.Context, sessionUUID uuid.UUID, userID uuid.UUID, broadcasterUserID string) error {
	// Check if session already active (fast path, no DB call)
	checkCh := make(chan bool, 1)
	e.cmdCh <- cmdCheckSession{sessionUUID: sessionUUID, replyCh: checkCh}
	if <-checkCh {
		replyCh := make(chan error, 1)
		e.cmdCh <- cmdResumeSession{sessionUUID: sessionUUID, replyCh: replyCh}
		return <-replyCh
	}

	// DB call happens in caller's goroutine, NOT the actor
	config, err := e.db.GetConfig(ctx, userID)
	if err != nil {
		return err
	}

	snapshot := models.ConfigSnapshot{
		ForTrigger:     config.ForTrigger,
		AgainstTrigger: config.AgainstTrigger,
		LeftLabel:      config.LeftLabel,
		RightLabel:     config.RightLabel,
		DecaySpeed:     config.DecaySpeed,
	}

	replyCh := make(chan error, 1)
	e.cmdCh <- cmdActivateSession{
		sessionUUID:       sessionUUID,
		broadcasterUserID: broadcasterUserID,
		config:            snapshot,
		replyCh:           replyCh,
	}
	return <-replyCh
}

func (e *Engine) ProcessVote(broadcasterUserID, twitchUserID, messageText string) {
	e.cmdCh <- cmdProcessVote{
		broadcasterUserID: broadcasterUserID,
		twitchUserID:      twitchUserID,
		messageText:       messageText,
	}
}

func (e *Engine) ResetSession(sessionUUID uuid.UUID) {
	e.cmdCh <- cmdResetSession{sessionUUID: sessionUUID}
}

func (e *Engine) MarkDisconnected(sessionUUID uuid.UUID) {
	e.cmdCh <- cmdMarkDisconnected{sessionUUID: sessionUUID}
}

func (e *Engine) CleanupOrphans(twitchManager TwitchManager) {
	e.cmdCh <- cmdCleanupOrphans{twitchManager: twitchManager}
}

func (e *Engine) GetSessionConfig(sessionUUID uuid.UUID) *models.ConfigSnapshot {
	replyCh := make(chan *models.ConfigSnapshot, 1)
	e.cmdCh <- cmdGetSessionConfig{sessionUUID: sessionUUID, replyCh: replyCh}
	return <-replyCh
}

func (e *Engine) UpdateSessionConfig(sessionUUID uuid.UUID, snapshot models.ConfigSnapshot) {
	e.cmdCh <- cmdUpdateSessionConfig{sessionUUID: sessionUUID, config: snapshot}
}

func (e *Engine) GetSessionValue(sessionUUID uuid.UUID) (float64, bool) {
	replyCh := make(chan sessionValueResult, 1)
	e.cmdCh <- cmdGetSessionValue{sessionUUID: sessionUUID, replyCh: replyCh}
	result := <-replyCh
	return result.value, result.ok
}

func (e *Engine) Stop() {
	doneCh := make(chan struct{})
	e.cmdCh <- cmdStop{doneCh: doneCh}
	<-doneCh
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
