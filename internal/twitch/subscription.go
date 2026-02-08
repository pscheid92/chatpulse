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
	"github.com/pscheid92/chatpulse/internal/models"
)

// subscriptionStore is the subset of database operations needed for subscription management.
type subscriptionStore interface {
	CreateEventSubSubscription(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error
	GetEventSubSubscription(ctx context.Context, userID uuid.UUID) (*models.EventSubSubscription, error)
	DeleteEventSubSubscription(ctx context.Context, userID uuid.UUID) error
}

// subscriptionAPIClient is the subset of TwitchClient used by SubscriptionManager.
type subscriptionAPIClient interface {
	CreateEventSubSubscription(ctx context.Context, subscriptionType, broadcasterUserID, botUserID, conduitID string) (*helix.EventSubSubscription, error)
	DeleteEventSubSubscription(ctx context.Context, subscriptionID string) error
}

// SubscriptionManager manages EventSub subscriptions backed by PostgreSQL.
// Subscriptions target a conduit, making them transport-agnostic.
type SubscriptionManager struct {
	client    subscriptionAPIClient
	db        subscriptionStore
	conduitID string
	botUserID string
}

// NewSubscriptionManager creates a new SubscriptionManager.
func NewSubscriptionManager(client subscriptionAPIClient, db subscriptionStore, conduitID, botUserID string) *SubscriptionManager {
	return &SubscriptionManager{
		client:    client,
		db:        db,
		conduitID: conduitID,
		botUserID: botUserID,
	}
}

// Subscribe creates an EventSub subscription for a broadcaster via the conduit.
// Idempotent: if a subscription already exists in DB, it returns nil.
func (sm *SubscriptionManager) Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error {
	// Check if subscription already exists
	existing, err := sm.db.GetEventSubSubscription(ctx, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing subscription: %w", err)
	}
	if existing != nil {
		log.Printf("EventSub subscription already exists for user %s", userID)
		return nil
	}

	// Create subscription via Twitch API
	sub, err := sm.client.CreateEventSubSubscription(ctx, "channel.chat.message", broadcasterUserID, sm.botUserID, sm.conduitID)
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
	if err := sm.db.CreateEventSubSubscription(ctx, userID, broadcasterUserID, sub.ID, sm.conduitID); err != nil {
		// Best effort: try to clean up the Twitch subscription
		if cleanupErr := sm.client.DeleteEventSubSubscription(ctx, sub.ID); cleanupErr != nil {
			log.Printf("Warning: failed to clean up Twitch subscription %s after DB persist failure: %v", sub.ID, cleanupErr)
		}
		return fmt.Errorf("failed to persist subscription: %w", err)
	}

	log.Printf("Subscribed to chat messages for broadcaster %s (subscription %s)", broadcasterUserID, sub.ID)
	return nil
}

// Unsubscribe removes an EventSub subscription for a user.
func (sm *SubscriptionManager) Unsubscribe(ctx context.Context, userID uuid.UUID) error {
	sub, err := sm.db.GetEventSubSubscription(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Nothing to unsubscribe
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	// Delete from Twitch API
	if err := sm.client.DeleteEventSubSubscription(ctx, sub.SubscriptionID); err != nil {
		log.Printf("Warning: failed to delete Twitch subscription %s: %v", sub.SubscriptionID, err)
		// Continue to delete from DB anyway
	}

	// Delete from DB
	if err := sm.db.DeleteEventSubSubscription(ctx, userID); err != nil {
		return fmt.Errorf("failed to delete subscription from DB: %w", err)
	}

	log.Printf("Unsubscribed from chat messages for user %s", userID)
	return nil
}
