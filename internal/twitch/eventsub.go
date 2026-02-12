package twitch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

const (
	defaultShardID     = "0"
	appTokenTimeout    = 15 * time.Second
	maxRetries         = 3
	initialBackoff     = 1 * time.Second
	rateLimitBackoff   = 30 * time.Second
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
		slog.Info("Found existing conduit", "conduit_id", conduit.ID)
	} else {
		conduit, err = m.client.CreateConduit(ctx, 1)
		if err != nil {
			return fmt.Errorf("failed to create conduit: %w", err)
		}
		if conduit == nil {
			return fmt.Errorf("no conduit returned from Twitch API")
		}
		slog.Info("Created conduit", "conduit_id", conduit.ID, "shard_count", conduit.ShardCount)
	}

	m.conduitID = conduit.ID

	// Update shard 0 with webhook transport.
	// Shard IDs are zero-indexed strings; we always create conduits with 1 shard.
	err = m.configureShard(ctx, conduit.ID)
	if err != nil {
		// Stale conduit from a previous crash — delete and create fresh
		slog.Error("Shard configuration failed, recreating conduit", "conduit_id", conduit.ID, "error", err)
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

	slog.Info("Conduit configured with webhook shard", "conduit_id", conduit.ID, "callback_url", m.callbackURL)
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
	slog.Info("Deleted conduit", "conduit_id", m.conduitID)
	return nil
}

// isEventSubRateLimited checks if an error is a 429 rate limit response
func isEventSubRateLimited(err error) bool {
	if apiErr, ok := errors.AsType[*helix.APIError](err); ok {
		return apiErr.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// isEventSubRetriable checks if an error is retriable (5xx or 429)
func isEventSubRetriable(err error) bool {
	if apiErr, ok := errors.AsType[*helix.APIError](err); ok {
		// 5xx errors are retriable, 429 is retriable with longer backoff
		// 4xx errors (except 429) are permanent (bad request, invalid broadcaster ID, etc.)
		return apiErr.StatusCode >= 500 || apiErr.StatusCode == http.StatusTooManyRequests
	}
	// Network errors, timeouts, context cancellations are retriable
	return true
}

// Subscribe creates an EventSub subscription for a broadcaster via the conduit.
// Idempotent: if a subscription already exists in DB, it returns nil.
// Retries transient failures (5xx, 429) with exponential backoff.
func (m *EventSubManager) Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error {
	// Check if subscription already exists
	existing, err := m.db.GetByUserID(ctx, userID)
	if err != nil && !errors.Is(err, domain.ErrSubscriptionNotFound) {
		return fmt.Errorf("failed to check existing subscription: %w", err)
	}
	if existing != nil {
		slog.Info("EventSub subscription already exists", "user_id", userID)
		return nil
	}

	// Retry loop with exponential backoff
	backoff := initialBackoff
	for attempt := 1; attempt <= maxRetries; attempt++ {
		sub, err := m.attemptSubscribe(ctx, userID, broadcasterUserID)
		if err == nil {
			// Success
			metrics.EventSubSubscribeAttemptsTotal.WithLabelValues("success").Inc()
			slog.Info("Subscribed to chat messages",
				"broadcaster_user_id", broadcasterUserID,
				"subscription_id", sub.ID,
				"attempt", attempt)
			return nil
		}

		// Check if error is retriable
		if isEventSubRateLimited(err) {
			// 429 rate limit - use longer backoff
			backoff = rateLimitBackoff
			slog.Warn("EventSub rate limited, backing off",
				"broadcaster_user_id", broadcasterUserID,
				"attempt", attempt,
				"backoff_seconds", backoff.Seconds())
		} else if !isEventSubRetriable(err) {
			// Permanent error (4xx except 429) - don't retry
			metrics.EventSubSubscribeAttemptsTotal.WithLabelValues("permanent_error").Inc()
			slog.Error("EventSub subscribe failed with permanent error",
				"broadcaster_user_id", broadcasterUserID,
				"error", err)
			return fmt.Errorf("EventSub subscribe failed (permanent): %w", err)
		}

		// Retry with backoff (unless last attempt)
		if attempt < maxRetries {
			slog.Warn("EventSub subscribe failed, retrying",
				"broadcaster_user_id", broadcasterUserID,
				"attempt", attempt,
				"backoff_seconds", backoff.Seconds(),
				"error", err)

			select {
			case <-time.After(backoff):
				backoff *= 2 // Exponential backoff
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			}
		} else {
			// Exhausted retries
			metrics.EventSubSubscribeAttemptsTotal.WithLabelValues("exhausted").Inc()
			slog.Error("EventSub subscribe failed after retries",
				"broadcaster_user_id", broadcasterUserID,
				"attempts", maxRetries,
				"error", err)
			return fmt.Errorf("EventSub subscribe failed after %d attempts: %w", maxRetries, err)
		}
	}

	return fmt.Errorf("EventSub subscribe failed after %d attempts", maxRetries)
}

// attemptSubscribe is a single attempt to create an EventSub subscription
func (m *EventSubManager) attemptSubscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) (*helix.EventSubSubscription, error) {
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
			slog.Info("EventSub subscription already exists on Twitch, treating as success", "broadcaster_user_id", broadcasterUserID)
			// Return a placeholder subscription (we don't have the ID, but that's okay)
			return &helix.EventSubSubscription{ID: "existing"}, nil
		}
		return nil, err
	}
	if sub == nil {
		return nil, fmt.Errorf("no subscription returned from Twitch API")
	}

	// Persist to DB
	if err := m.db.Create(ctx, userID, broadcasterUserID, sub.ID, m.conduitID); err != nil {
		// Best effort: try to clean up the Twitch subscription
		if cleanupErr := m.client.DeleteEventSubSubscription(ctx, sub.ID); cleanupErr != nil {
			slog.Error("Failed to clean up Twitch subscription after DB persist failure", "subscription_id", sub.ID, "error", cleanupErr)
		}
		return nil, fmt.Errorf("failed to persist subscription: %w", err)
	}

	return sub, nil
}

// Unsubscribe removes an EventSub subscription for a user.
// Retries transient failures when deleting from Twitch API.
func (m *EventSubManager) Unsubscribe(ctx context.Context, userID uuid.UUID) error {
	sub, err := m.db.GetByUserID(ctx, userID)
	if errors.Is(err, domain.ErrSubscriptionNotFound) {
		return nil // Nothing to unsubscribe
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	// Delete from Twitch API with retry
	backoff := initialBackoff
	deleted := false
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := m.client.DeleteEventSubSubscription(ctx, sub.SubscriptionID)
		if err == nil {
			deleted = true
			break
		}

		// Check if error is retriable
		if !isEventSubRetriable(err) {
			// Permanent error - log and continue (still delete from DB)
			slog.Error("EventSub unsubscribe failed with permanent error",
				"subscription_id", sub.SubscriptionID,
				"error", err)
			break
		}

		// Retry with backoff (unless last attempt)
		if attempt < maxRetries {
			if isEventSubRateLimited(err) {
				backoff = rateLimitBackoff
			}

			slog.Warn("EventSub unsubscribe failed, retrying",
				"subscription_id", sub.SubscriptionID,
				"attempt", attempt,
				"backoff_seconds", backoff.Seconds(),
				"error", err)

			select {
			case <-time.After(backoff):
				backoff *= 2
			case <-ctx.Done():
				// Context cancelled - still try to delete from DB
				slog.Warn("Context cancelled during unsubscribe retry, will delete from DB anyway")
				break
			}
		} else {
			// Exhausted retries - log as orphan and continue (delete from DB anyway)
			slog.Error("EventSub unsubscribe failed after retries, subscription may be orphaned",
				"subscription_id", sub.SubscriptionID,
				"attempts", maxRetries,
				"error", err)
		}
	}

	// Delete from DB (always attempt, even if Twitch delete failed)
	if err := m.db.Delete(ctx, userID); err != nil {
		return fmt.Errorf("failed to delete subscription from DB: %w", err)
	}

	if deleted {
		slog.Info("Unsubscribed from chat messages", "user_id", userID, "subscription_id", sub.SubscriptionID)
	} else {
		slog.Warn("Deleted subscription from DB but Twitch unsubscribe may have failed",
			"user_id", userID,
			"subscription_id", sub.SubscriptionID)
	}

	return nil
}
