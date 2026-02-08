package sentiment

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/models"
)

// ConfigStore is the subset of database operations needed by the engine.
type ConfigStore interface {
	GetConfig(ctx context.Context, userID uuid.UUID) (*models.Config, error)
}

type Broadcaster interface {
	Broadcast(sessionUUID uuid.UUID, value float64, status string)
}

type TwitchManager interface {
	Unsubscribe(ctx context.Context, userID uuid.UUID) error
}

const (
	pruneDebounceEvery = 1000 // ticks (~50 seconds at 50ms/tick)
	orphanMaxAge       = 30 * time.Second
	decayMinIntervalMs = 40
)

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

type Engine struct {
	cmdCh chan engineCmd
	db    ConfigStore
	store SessionStateStore
	clock clockwork.Clock
	// localSessions tracks sessions activated on this instance (for tick/decay).
	// Maps session UUID to cached config (needed for decay factor calculation).
	localSessions       map[uuid.UUID]models.ConfigSnapshot
	broadcaster         Broadcaster
	broadcastAfterDecay bool
	stopCh              chan struct{}
}

func NewEngine(store SessionStateStore, db ConfigStore, clock clockwork.Clock, broadcastAfterDecay bool) *Engine {
	return &Engine{
		cmdCh:               make(chan engineCmd, 512),
		db:                  db,
		store:               store,
		clock:               clock,
		localSessions:       make(map[uuid.UUID]models.ConfigSnapshot),
		broadcastAfterDecay: broadcastAfterDecay,
		stopCh:              make(chan struct{}),
	}
}

// SetBroadcaster sets the broadcaster for the engine. Used to resolve circular
// dependency where Engine needs Hub for broadcasting but Hub needs Engine for
// callbacks. Must be called before Start().
func (e *Engine) SetBroadcaster(b Broadcaster) {
	e.cmdCh <- cmdSetBroadcaster{b: b}
}

// Start begins the engine's background goroutines (ticker and actor).
// IMPORTANT: Call SetBroadcaster() before Start() to ensure broadcasts work correctly.
func (e *Engine) Start() {
	go e.tickerLoop()
	go e.run()
}

func (e *Engine) run() {
	ctx := context.Background()
	tickCount := 0
	for cmd := range e.cmdCh {
		switch c := cmd.(type) {
		case cmdSetBroadcaster:
			e.broadcaster = c.b
			log.Printf("Broadcaster set for engine")

		case cmdCheckSession:
			exists, err := e.store.SessionExists(ctx, c.sessionUUID)
			if err != nil {
				log.Printf("SessionExists error for %s: %v", c.sessionUUID, err)
			}
			c.replyCh <- exists

		case cmdResumeSession:
			if err := e.store.ResumeSession(ctx, c.sessionUUID); err != nil {
				c.replyCh <- err
				break
			}
			// Ensure session is tracked locally for tick/decay
			if _, exists := e.localSessions[c.sessionUUID]; !exists {
				config, err := e.store.GetSessionConfig(ctx, c.sessionUUID)
				if err == nil && config != nil {
					e.localSessions[c.sessionUUID] = *config
				}
			}
			log.Printf("Session %s resumed", c.sessionUUID)
			c.replyCh <- nil

		case cmdActivateSession:
			if err := e.store.ActivateSession(ctx, c.sessionUUID, c.broadcasterUserID, c.config); err != nil {
				c.replyCh <- err
				break
			}
			e.localSessions[c.sessionUUID] = c.config
			log.Printf("Session %s activated (broadcaster %s)", c.sessionUUID, c.broadcasterUserID)
			c.replyCh <- nil

		case cmdProcessVote:
			e.handleProcessVote(ctx, c)

		case cmdTick:
			e.handleTick(ctx)
			tickCount++
			if tickCount%pruneDebounceEvery == 0 {
				if pruner, ok := e.store.(DebouncePruner); ok {
					if err := pruner.PruneDebounce(ctx); err != nil {
						log.Printf("PruneDebounce error: %v", err)
					}
				}
			}

		case cmdResetSession:
			if err := e.store.ResetValue(ctx, c.sessionUUID); err != nil {
				log.Printf("ResetValue error for session %s: %v", c.sessionUUID, err)
			} else {
				log.Printf("Session %s reset", c.sessionUUID)
			}

		case cmdMarkDisconnected:
			if err := e.store.MarkDisconnected(ctx, c.sessionUUID); err != nil {
				log.Printf("MarkDisconnected error for session %s: %v", c.sessionUUID, err)
			} else {
				log.Printf("Session %s marked as disconnected", c.sessionUUID)
			}

		case cmdCleanupOrphans:
			e.handleCleanupOrphans(ctx, c.twitchManager)

		case cmdUpdateSessionConfig:
			if err := e.store.UpdateConfig(ctx, c.sessionUUID, c.config); err != nil {
				log.Printf("UpdateConfig error for session %s: %v", c.sessionUUID, err)
			} else {
				if _, exists := e.localSessions[c.sessionUUID]; exists {
					e.localSessions[c.sessionUUID] = c.config
				}
				log.Printf("Session %s config updated", c.sessionUUID)
			}

		case cmdGetSessionConfig:
			config, err := e.store.GetSessionConfig(ctx, c.sessionUUID)
			if err != nil {
				log.Printf("GetSessionConfig error for %s: %v", c.sessionUUID, err)
			}
			c.replyCh <- config

		case cmdGetSessionValue:
			value, exists, err := e.store.GetSessionValue(ctx, c.sessionUUID)
			if err != nil {
				log.Printf("GetSessionValue error for %s: %v", c.sessionUUID, err)
			}
			c.replyCh <- sessionValueResult{value: value, ok: exists}

		case cmdStop:
			close(e.stopCh)
			close(c.doneCh)
			return
		}
	}
}

func (e *Engine) handleProcessVote(ctx context.Context, c cmdProcessVote) {
	newValue, applied := ProcessVote(ctx, e.store, c.broadcasterUserID, c.twitchUserID, c.messageText)
	if applied {
		log.Printf("Vote processed: user=%s, new value=%.2f", c.twitchUserID, newValue)
	}
}

func (e *Engine) handleTick(ctx context.Context) {
	for id, config := range e.localSessions {
		decayFactor := math.Exp(-config.DecaySpeed * 0.05)
		nowMs := e.clock.Now().UnixMilli()
		newValue, err := e.store.ApplyDecay(ctx, id, decayFactor, nowMs, decayMinIntervalMs)
		if err != nil {
			log.Printf("Decay error for session %s: %v", id, err)
			continue
		}
		if e.broadcastAfterDecay {
			e.broadcaster.Broadcast(id, newValue, "active")
		}
	}
}

func (e *Engine) handleCleanupOrphans(ctx context.Context, twitchManager TwitchManager) {
	orphans, err := e.store.ListOrphans(ctx, orphanMaxAge)
	if err != nil {
		log.Printf("ListOrphans error: %v", err)
		return
	}

	for _, id := range orphans {
		if err := e.store.DeleteSession(ctx, id); err != nil {
			log.Printf("DeleteSession error for %s: %v", id, err)
		}
		delete(e.localSessions, id)
	}

	go func() {
		ctx := context.Background()
		for _, id := range orphans {
			if err := twitchManager.Unsubscribe(ctx, id); err != nil {
				log.Printf("Failed to unsubscribe orphan session %s: %v", id, err)
			}
			log.Printf("Cleaned up orphan session %s", id)
		}
	}()
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
