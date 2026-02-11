package twitch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const (
	defaultShardID  = "0"
	appTokenTimeout = 15 * time.Second
)

// subscriptionStore is the subset of database operations needed for subscription management.
type subscriptionStore interface {
	Create(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error
	GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.EventSubSubscription, error)
	Delete(ctx context.Context, userID uuid.UUID) error
}

// EventSubManager manages the lifecycle of a Twitch EventSub conduit and its subscriptions.
type EventSubManager struct {
	client      *helix.Client
	db          subscriptionStore
	conduitID   string
	callbackURL string
	secret      string
	botUserID   string
}

// NewEventSubManager creates a new EventSubManager. It validates credentials by
// requesting an app access token (fail-fast if client ID/secret are invalid).
func NewEventSubManager(clientID, clientSecret string, db subscriptionStore, callbackURL, secret, botUserID string) (*EventSubManager, error) {
	auth := helix.NewAuthClient(helix.AuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})

	ctx, cancel := context.WithTimeout(context.Background(), appTokenTimeout)
	defer cancel()

	if _, err := auth.GetAppAccessToken(ctx); err != nil {
		return nil, fmt.Errorf("failed to get app access token: %w", err)
	}

	return &EventSubManager{
		client:      helix.NewClient(clientID, auth),
		db:          db,
		callbackURL: callbackURL,
		secret:      secret,
		botUserID:   botUserID,
	}, nil
}

// Setup creates or finds an existing conduit, then configures its shard
// with a webhook transport pointing at callbackURL.
func (m *EventSubManager) Setup(ctx context.Context) error {
	// Check for existing conduit first (idempotent restart)
	resp, err := m.client.GetConduits(ctx)
	if err != nil {
		return fmt.Errorf("failed to list conduits: %w", err)
	}

	var conduit *helix.Conduit
	if len(resp.Data) > 0 {
		conduit = &resp.Data[0]
		log.Printf("Found existing conduit %s", conduit.ID)
	} else {
		conduit, err = m.client.CreateConduit(ctx, 1)
		if err != nil {
			return fmt.Errorf("failed to create conduit: %w", err)
		}
		if conduit == nil {
			return fmt.Errorf("no conduit returned from Twitch API")
		}
		log.Printf("Created conduit %s with %d shards", conduit.ID, conduit.ShardCount)
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
		if conduit == nil {
			return fmt.Errorf("no conduit returned from Twitch API")
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
	if err := m.client.DeleteConduit(ctx, m.conduitID); err != nil {
		return fmt.Errorf("failed to delete conduit: %w", err)
	}
	log.Printf("Deleted conduit %s", m.conduitID)
	return nil
}

// Subscribe creates an EventSub subscription for a broadcaster via the conduit.
// Idempotent: if a subscription already exists in DB, it returns nil.
func (m *EventSubManager) Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error {
	// Check if subscription already exists
	existing, err := m.db.GetByUserID(ctx, userID)
	if err != nil && !errors.Is(err, domain.ErrSubscriptionNotFound) {
		return fmt.Errorf("failed to check existing subscription: %w", err)
	}
	if existing != nil {
		log.Printf("EventSub subscription already exists for user %s", userID)
		return nil
	}

	// Create subscription via Twitch API
	sub, err := m.client.CreateEventSubSubscription(ctx, &helix.CreateEventSubSubscriptionParams{
		Type:    "channel.chat.message",
		Version: "1",
		Condition: map[string]string{
			"broadcaster_user_id": broadcasterUserID,
			"user_id":             m.botUserID,
		},
		Transport: helix.CreateEventSubTransport{
			Method:    "conduit",
			ConduitID: m.conduitID,
		},
	})
	if err != nil {
		// 409 Conflict means the subscription already exists on Twitch (e.g. DB record
		// was lost but Twitch still has it). Treat as success — the subscription is active.
		if apiErr, ok := errors.AsType[*helix.APIError](err); ok && apiErr.StatusCode == http.StatusConflict {
			log.Printf("EventSub subscription already exists on Twitch for broadcaster %s, treating as success", broadcasterUserID)
			return nil
		}
		return fmt.Errorf("failed to create EventSub subscription: %w", err)
	}
	if sub == nil {
		return fmt.Errorf("no subscription returned from Twitch API")
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
	if errors.Is(err, domain.ErrSubscriptionNotFound) {
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
