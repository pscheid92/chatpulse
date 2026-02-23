package twitch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/retry"
	"github.com/pscheid92/uuid"
)

const (
	defaultShardID        = "0"
	appTokenTimeout       = 15 * time.Second
	retryInitialBackoff   = 1 * time.Second
	retryRateLimitBackoff = 30 * time.Second
)

type EventSubManager struct {
	client     *helix.Client
	repository domain.EventSubRepository

	conduitID   string
	callbackURL string
	secret      string
	botUserID   string
}

func NewEventSubManager(clientID, clientSecret string, repository domain.EventSubRepository, callbackURL, secret, botUserID string) (*EventSubManager, error) {
	ctx, cancel := context.WithTimeout(context.Background(), appTokenTimeout)
	defer cancel()

	authConfig := helix.AuthConfig{ClientID: clientID, ClientSecret: clientSecret}
	auth := helix.NewAuthClient(authConfig)
	client := helix.NewClient(clientID, auth)

	if _, err := auth.GetAppAccessToken(ctx); err != nil {
		return nil, fmt.Errorf("failed to get app access token: %w", err)
	}

	esm := EventSubManager{
		client:      client,
		repository:  repository,
		callbackURL: callbackURL,
		secret:      secret,
		botUserID:   botUserID,
	}
	return &esm, nil
}

func (m *EventSubManager) Setup(ctx context.Context) error {
	conduit, err := m.findOrCreateConduit(ctx)
	if err != nil {
		return err
	}

	if err := m.configureShard(ctx, conduit.ID); err != nil {
		conduit, err = m.recreateConduit(ctx, conduit.ID, err)
		if err != nil {
			return err
		}
	}

	m.conduitID = conduit.ID
	slog.Info("Conduit configured with webhook shard", "conduit_id", conduit.ID, "callback_url", m.callbackURL)
	return nil
}

func (m *EventSubManager) findOrCreateConduit(ctx context.Context) (*helix.Conduit, error) {
	resp, err := m.client.GetConduits(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list conduits: %w", err)
	}

	if len(resp.Data) > 0 {
		slog.Info("Found existing conduit", "conduit_id", resp.Data[0].ID)
		return &resp.Data[0], nil
	}

	return m.createConduit(ctx)
}

func (m *EventSubManager) createConduit(ctx context.Context) (*helix.Conduit, error) {
	conduit, err := m.client.CreateConduit(ctx, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to create conduit: %w", err)
	}
	if conduit == nil {
		return nil, errors.New("no conduit returned from Twitch API")
	}

	slog.Info("Created conduit", "conduit_id", conduit.ID, "shard_count", conduit.ShardCount)
	return conduit, nil
}

func (m *EventSubManager) configureShard(ctx context.Context, conduitID string) error {
	shard := helix.UpdateConduitShardParams{
		ID: defaultShardID,
		Transport: helix.UpdateConduitShardTransport{
			Method:   "webhook",
			Callback: m.callbackURL,
			Secret:   m.secret,
		},
	}

	params := helix.UpdateConduitShardsParams{ConduitID: conduitID, Shards: []helix.UpdateConduitShardParams{shard}}
	_, err := m.client.UpdateConduitShards(ctx, &params)
	if err != nil {
		return fmt.Errorf("failed to update conduit shards: %w", err)
	}

	return nil
}

func (m *EventSubManager) recreateConduit(ctx context.Context, staleID string, shardErr error) (*helix.Conduit, error) {
	slog.Error("Shard configuration failed, recreating conduit", "conduit_id", staleID, "error", shardErr)

	if err := m.client.DeleteConduit(ctx, staleID); err != nil {
		return nil, fmt.Errorf("failed to delete stale conduit: %w", err)
	}

	conduit, err := m.createConduit(ctx)
	if err != nil {
		return nil, err
	}

	if err := m.configureShard(ctx, conduit.ID); err != nil {
		return nil, fmt.Errorf("failed to configure shard on new conduit: %w", err)
	}

	return conduit, nil
}

func (m *EventSubManager) Cleanup(ctx context.Context) error {
	if m.conduitID == "" {
		return nil
	}

	// Delete DB subscription records tied to this conduit before deleting the conduit.
	// Deleting the conduit on Twitch implicitly removes all its subscriptions,
	// so the DB records would become stale and block re-subscription on next startup.
	if err := m.repository.DeleteByConduitID(ctx, m.conduitID); err != nil {
		slog.Error("Failed to delete stale subscription records", "conduit_id", m.conduitID, "error", err)
	}

	if err := m.client.DeleteConduit(ctx, m.conduitID); err != nil {
		return fmt.Errorf("failed to delete conduit: %w", err)
	}

	slog.Info("Deleted conduit", "conduit_id", m.conduitID)
	return nil
}

func (m *EventSubManager) Subscribe(ctx context.Context, streamerID uuid.UUID, broadcasterUserID string) error {
	existing, err := m.repository.GetByStreamerID(ctx, streamerID)
	if err == nil {
		if existing.ConduitID == m.conduitID {
			slog.Info("EventSub subscription already exists", "streamer_id", streamerID)
			return nil
		}
		// Subscription belongs to a stale conduit — delete and re-create
		slog.Info("Deleting stale EventSub subscription", "streamer_id", streamerID, "old_conduit", existing.ConduitID, "current_conduit", m.conduitID)
		if delErr := m.repository.Delete(ctx, streamerID); delErr != nil {
			return fmt.Errorf("failed to delete stale subscription: %w", delErr)
		}
	} else if !errors.Is(err, domain.ErrSubscriptionNotFound) {
		return fmt.Errorf("failed to check existing subscription: %w", err)
	}

	p := getRetryPolicy()
	p.OnRetry = func(attempt int, err error, backoff time.Duration) {
		slog.Warn("EventSub subscribe failed, retrying", "broadcaster_user_id", broadcasterUserID, "attempt", attempt, "backoff_seconds", backoff.Seconds(), "error", err)
	}

	workFunc := func() (*helix.EventSubSubscription, error) {
		return m.attemptSubscribe(ctx, streamerID, broadcasterUserID)
	}
	sub, err := retry.Do(ctx, p, classifyEventSubError, workFunc)
	if err != nil {
		label := "after retries"
		if _, ok := errors.AsType[*retry.PermanentError](err); ok {
			label = "permanent"
		}

		slog.Error("EventSub subscribe failed", "broadcaster_user_id", broadcasterUserID, "cause", label, "error", err)
		return fmt.Errorf("EventSub subscribe failed (%s): %w", label, err)
	}

	slog.Info("Subscribed to chat messages", "broadcaster_user_id", broadcasterUserID, "subscription_id", sub.ID)
	return nil
}

func (m *EventSubManager) attemptSubscribe(ctx context.Context, streamerID uuid.UUID, broadcasterUserID string) (*helix.EventSubSubscription, error) {
	params := helix.CreateEventSubSubscriptionParams{
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
	}
	sub, err := m.client.CreateEventSubSubscription(ctx, &params)
	if err != nil {
		if apiErr, ok := errors.AsType[*helix.APIError](err); ok && apiErr.StatusCode == http.StatusConflict {
			slog.Info("EventSub subscription already exists on Twitch, recovering", "broadcaster_user_id", broadcasterUserID)
			return m.findExistingSubscription(ctx, broadcasterUserID)
		}
		return nil, fmt.Errorf("failed to create EventSub subscription: %w", err)
	}
	if sub == nil {
		return nil, errors.New("no subscription returned from Twitch API")
	}

	if dbErr := m.repository.Create(ctx, streamerID, sub.ID, m.conduitID); dbErr != nil {
		// Compensate: clean up Twitch subscription after DB persist failure
		if cleanupErr := m.client.DeleteEventSubSubscription(ctx, sub.ID); cleanupErr != nil {
			slog.Error("Failed to clean up Twitch subscription after DB persist failure", "subscription_id", sub.ID, "error", cleanupErr)
		}
		return nil, fmt.Errorf("failed to persist subscription: %w", dbErr)
	}

	return sub, nil
}

func (m *EventSubManager) findExistingSubscription(ctx context.Context, broadcasterUserID string) (*helix.EventSubSubscription, error) {
	params := helix.GetEventSubSubscriptionsParams{Type: "channel.chat.message"}

	for {
		resp, err := m.client.GetEventSubSubscriptions(ctx, &params)
		if err != nil {
			return nil, fmt.Errorf("failed to list subscriptions for 409 recovery: %w", err)
		}

		for _, sub := range resp.Data {
			if sub.Condition["broadcaster_user_id"] == broadcasterUserID && sub.Condition["user_id"] == m.botUserID {
				return &sub, nil
			}
		}

		if resp.Pagination == nil || resp.Pagination.Cursor == "" {
			break
		}
		params.PaginationParams = &helix.PaginationParams{After: resp.Pagination.Cursor}
	}

	return nil, fmt.Errorf("subscription not found on Twitch despite 409 conflict (broadcaster_user_id=%s)", broadcasterUserID)
}

func (m *EventSubManager) Unsubscribe(ctx context.Context, streamerID uuid.UUID) error {
	sub, err := m.repository.GetByStreamerID(ctx, streamerID)
	if errors.Is(err, domain.ErrSubscriptionNotFound) {
		// POSITIVE: Subscription already gone
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	twitchClean := m.deleteSubscriptionFromTwitch(ctx, sub.SubscriptionID)

	if err := m.repository.Delete(ctx, streamerID); err != nil {
		return fmt.Errorf("failed to delete subscription from DB: %w", err)
	}

	if twitchClean {
		slog.Info("Unsubscribed from chat messages", "streamer_id", streamerID, "subscription_id", sub.SubscriptionID)
	} else {
		slog.Warn("Deleted subscription from DB but Twitch unsubscribe may have failed", "streamer_id", streamerID, "subscription_id", sub.SubscriptionID)
	}

	return nil
}

func (m *EventSubManager) deleteSubscriptionFromTwitch(ctx context.Context, subscriptionID string) bool {
	p := getRetryPolicy()
	p.OnRetry = func(attempt int, retryErr error, backoff time.Duration) {
		slog.Warn("EventSub unsubscribe failed, retrying", "subscription_id", subscriptionID, "attempt", attempt, "backoff_seconds", backoff.Seconds(), "error", retryErr)
	}

	err := retry.DoVoid(ctx, p, classifyEventSubError, func() error {
		return m.client.DeleteEventSubSubscription(ctx, subscriptionID)
	})
	if err != nil {
		slog.Error("EventSub unsubscribe failed, subscription may be orphaned", "subscription_id", subscriptionID, "error", err)
		return false
	}
	return true
}

func classifyEventSubError(err error) retry.Action {
	apiErr, ok := errors.AsType[*helix.APIError](err)
	if !ok {
		return retry.Retry
	}

	switch {
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return retry.After
	case apiErr.StatusCode >= 500:
		return retry.Retry
	default:
		return retry.Stop
	}
}

func getRetryPolicy() retry.Policy {
	return retry.Policy{
		MaxAttempts:      3,
		InitialBackoff:   retryInitialBackoff,
		RateLimitBackoff: retryRateLimitBackoff,
	}
}
