package twitch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const defaultShardID = "0"

// eventsubAPIClient is the subset of TwitchClient used by EventSubManager.
type eventsubAPIClient interface {
	// Conduit operations
	CreateConduit(ctx context.Context, shardCount int) (*helix.Conduit, error)
	GetConduits(ctx context.Context) ([]helix.Conduit, error)
	UpdateConduitShards(ctx context.Context, params *helix.UpdateConduitShardsParams) (*helix.UpdateConduitShardsResponse, error)
	DeleteConduit(ctx context.Context, conduitID string) error
	// Subscription operations
	CreateEventSubSubscription(ctx context.Context, subscriptionType, broadcasterUserID, botUserID, conduitID string) (*helix.EventSubSubscription, error)
	DeleteEventSubSubscription(ctx context.Context, subscriptionID string) error
}

// subscriptionStore is the subset of database operations needed for subscription management.
type subscriptionStore interface {
	Create(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error
	GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.EventSubSubscription, error)
	Delete(ctx context.Context, userID uuid.UUID) error
}

// EventSubManager manages the lifecycle of a Twitch EventSub conduit and its subscriptions.
type EventSubManager struct {
	client      eventsubAPIClient
	db          subscriptionStore
	conduitID   string
	callbackURL string
	secret      string
	botUserID   string
}

// NewEventSubManager creates a new EventSubManager.
func NewEventSubManager(client eventsubAPIClient, db subscriptionStore, callbackURL, secret, botUserID string) *EventSubManager {
	return &EventSubManager{
		client:      client,
		db:          db,
		callbackURL: callbackURL,
		secret:      secret,
		botUserID:   botUserID,
	}
}

// Setup creates or finds an existing conduit, then configures its shard
// with a webhook transport pointing at callbackURL.
func (m *EventSubManager) Setup(ctx context.Context) error {
	// Check for existing conduit first (idempotent restart)
	conduits, err := m.client.GetConduits(ctx)
	if err != nil {
		return fmt.Errorf("failed to list conduits: %w", err)
	}

	var conduit *helix.Conduit
	if len(conduits) > 0 {
		conduit = &conduits[0]
		log.Printf("Found existing conduit %s", conduit.ID)
	} else {
		conduit, err = m.client.CreateConduit(ctx, 1)
		if err != nil {
			return fmt.Errorf("failed to create conduit: %w", err)
		}
	}

	m.conduitID = conduit.ID

	// Update shard 0 with webhook transport.
	// Shard IDs are zero-indexed strings; we always create conduits with 1 shard.
	err = m.configureShard(ctx, conduit.ID)
	if err != nil {
		// Stale conduit from a previous crash — delete and create fresh
		log.Printf("Shard configuration failed for conduit %s, recreating: %v", conduit.ID, err)
		if delErr := m.client.DeleteConduit(ctx, conduit.ID); delErr != nil {
			return fmt.Errorf("failed to delete stale conduit: %w", delErr)
		}
		conduit, err = m.client.CreateConduit(ctx, 1)
		if err != nil {
			return fmt.Errorf("failed to recreate conduit: %w", err)
		}
		m.conduitID = conduit.ID
		if err = m.configureShard(ctx, conduit.ID); err != nil {
			return fmt.Errorf("failed to configure shard on new conduit: %w", err)
		}
	}

	log.Printf("Conduit %s configured with webhook shard pointing to %s", conduit.ID, m.callbackURL)
	return nil
}

func (m *EventSubManager) configureShard(ctx context.Context, conduitID string) error {
	_, err := m.client.UpdateConduitShards(ctx, &helix.UpdateConduitShardsParams{
		ConduitID: conduitID,
		Shards: []helix.UpdateConduitShardParams{
			{
				ID: defaultShardID,
				Transport: helix.UpdateConduitShardTransport{
					Method:   "webhook",
					Callback: m.callbackURL,
					Secret:   m.secret,
				},
			},
		},
	})
	return err
}

// Cleanup deletes the conduit. Call on graceful shutdown.
func (m *EventSubManager) Cleanup(ctx context.Context) error {
	if m.conduitID == "" {
		return nil
	}
	return m.client.DeleteConduit(ctx, m.conduitID)
}

// Subscribe creates an EventSub subscription for a broadcaster via the conduit.
// Idempotent: if a subscription already exists in DB, it returns nil.
func (m *EventSubManager) Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error {
	// Check if subscription already exists
	existing, err := m.db.GetByUserID(ctx, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing subscription: %w", err)
	}
	if existing != nil {
		log.Printf("EventSub subscription already exists for user %s", userID)
		return nil
	}

	// Create subscription via Twitch API
	sub, err := m.client.CreateEventSubSubscription(ctx, "channel.chat.message", broadcasterUserID, m.botUserID, m.conduitID)
	if err != nil {
		// 409 Conflict means the subscription already exists on Twitch (e.g. DB record
		// was lost but Twitch still has it). Treat as success — the subscription is active.
		var apiErr *helix.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			log.Printf("EventSub subscription already exists on Twitch for broadcaster %s, treating as success", broadcasterUserID)
			return nil
		}
		return fmt.Errorf("failed to create EventSub subscription: %w", err)
	}

	// Persist to DB
	if err := m.db.Create(ctx, userID, broadcasterUserID, sub.ID, m.conduitID); err != nil {
		// Best effort: try to clean up the Twitch subscription
		if cleanupErr := m.client.DeleteEventSubSubscription(ctx, sub.ID); cleanupErr != nil {
			log.Printf("Warning: failed to clean up Twitch subscription %s after DB persist failure: %v", sub.ID, cleanupErr)
		}
		return fmt.Errorf("failed to persist subscription: %w", err)
	}

	log.Printf("Subscribed to chat messages for broadcaster %s (subscription %s)", broadcasterUserID, sub.ID)
	return nil
}

// Unsubscribe removes an EventSub subscription for a user.
func (m *EventSubManager) Unsubscribe(ctx context.Context, userID uuid.UUID) error {
	sub, err := m.db.GetByUserID(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Nothing to unsubscribe
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	// Delete from Twitch API
	if err := m.client.DeleteEventSubSubscription(ctx, sub.SubscriptionID); err != nil {
		log.Printf("Warning: failed to delete Twitch subscription %s: %v", sub.SubscriptionID, err)
		// Continue to delete from DB anyway
	}

	// Delete from DB
	if err := m.db.Delete(ctx, userID); err != nil {
		return fmt.Errorf("failed to delete subscription from DB: %w", err)
	}

	log.Printf("Unsubscribed from chat messages for user %s", userID)
	return nil
}
