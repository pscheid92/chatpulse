package twitch

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Its-donkey/kappopher/helix"
)

const appTokenTimeout = 15 * time.Second

// TwitchClient wraps a Kappopher helix client for Twitch API operations.
// Uses app-scoped client credentials for conduit management and EventSub CRUD.
type TwitchClient struct {
	appClient *helix.Client
}

// NewTwitchClient creates a TwitchClient with app-level capabilities.
// It obtains an app access token (client credentials) for conduit management.
func NewTwitchClient(clientID, clientSecret string) (*TwitchClient, error) {
	appAuth := helix.NewAuthClient(helix.AuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})

	// Get app access token for conduit management
	ctx, cancel := context.WithTimeout(context.Background(), appTokenTimeout)
	defer cancel()

	_, err := appAuth.GetAppAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get app access token: %w", err)
	}

	appClient := helix.NewClient(clientID, appAuth)

	return &TwitchClient{
		appClient: appClient,
	}, nil
}

// --- Conduit operations (app-scoped) ---

// CreateConduit creates a new Twitch conduit with the given shard count.
func (tc *TwitchClient) CreateConduit(ctx context.Context, shardCount int) (*helix.Conduit, error) {
	conduit, err := tc.appClient.CreateConduit(ctx, shardCount)
	if err != nil {
		return nil, fmt.Errorf("failed to create conduit: %w", err)
	}
	log.Printf("Created conduit %s with %d shards", conduit.ID, conduit.ShardCount)
	return conduit, nil
}

// GetConduits returns all conduits owned by the app.
func (tc *TwitchClient) GetConduits(ctx context.Context) ([]helix.Conduit, error) {
	resp, err := tc.appClient.GetConduits(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get conduits: %w", err)
	}
	return resp.Data, nil
}

// UpdateConduitShards configures the transport (webhook) for conduit shards.
func (tc *TwitchClient) UpdateConduitShards(ctx context.Context, params *helix.UpdateConduitShardsParams) (*helix.UpdateConduitShardsResponse, error) {
	resp, err := tc.appClient.UpdateConduitShards(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to update conduit shards: %w", err)
	}
	return resp, nil
}

// DeleteConduit deletes a conduit by ID.
func (tc *TwitchClient) DeleteConduit(ctx context.Context, conduitID string) error {
	if err := tc.appClient.DeleteConduit(ctx, conduitID); err != nil {
		return fmt.Errorf("failed to delete conduit: %w", err)
	}
	log.Printf("Deleted conduit %s", conduitID)
	return nil
}

// --- EventSub subscription operations (app-scoped for conduit transport) ---

// CreateEventSubSubscription creates an EventSub subscription targeting a conduit.
func (tc *TwitchClient) CreateEventSubSubscription(ctx context.Context, subscriptionType, broadcasterUserID, botUserID, conduitID string) (*helix.EventSubSubscription, error) {
	sub, err := tc.appClient.CreateEventSubSubscription(ctx, &helix.CreateEventSubSubscriptionParams{
		Type:    subscriptionType,
		Version: "1",
		Condition: map[string]string{
			"broadcaster_user_id": broadcasterUserID,
			"user_id":             botUserID,
		},
		Transport: helix.CreateEventSubTransport{
			Method:    "conduit",
			ConduitID: conduitID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create eventsub subscription: %w", err)
	}

	log.Printf("Created EventSub subscription %s for broadcaster %s via conduit %s", sub.ID, broadcasterUserID, conduitID)
	return sub, nil
}

// DeleteEventSubSubscription deletes an EventSub subscription by ID.
func (tc *TwitchClient) DeleteEventSubSubscription(ctx context.Context, subscriptionID string) error {
	if err := tc.appClient.DeleteEventSubSubscription(ctx, subscriptionID); err != nil {
		return fmt.Errorf("failed to delete eventsub subscription: %w", err)
	}
	return nil
}
