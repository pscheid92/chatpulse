package twitch

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/nicklaw5/helix/v2"
	"github.com/pscheid92/twitch-tow/internal/database"
	"github.com/pscheid92/twitch-tow/internal/models"
)

// tokenRefresher is an interface for refreshing user tokens
type tokenRefresher interface {
	EnsureValidToken(ctx context.Context, userID uuid.UUID) (*models.User, error)
}

type HelixClient struct {
	mu             sync.Mutex
	client         *helix.Client
	tokenRefresher tokenRefresher
}

func NewHelixClient(db *database.DB, clientID, clientSecret string) (*HelixClient, error) {
	client, err := helix.NewClient(&helix.Options{
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create helix client: %w", err)
	}

	return &HelixClient{
		client:         client,
		tokenRefresher: NewTokenRefresher(db, clientID, clientSecret),
	}, nil
}

func (hc *HelixClient) CreateEventSubSubscription(ctx context.Context, userID uuid.UUID, subscriptionType, broadcasterUserID, sessionID string) (string, error) {
	user, err := hc.tokenRefresher.EnsureValidToken(ctx, userID)
	if err != nil {
		return "", err
	}

	hc.mu.Lock()
	hc.client.SetUserAccessToken(user.AccessToken)
	resp, err := hc.client.CreateEventSubSubscription(&helix.EventSubSubscription{
		Type:    subscriptionType,
		Version: "1",
		Condition: helix.EventSubCondition{
			BroadcasterUserID: broadcasterUserID,
			UserID:            broadcasterUserID,
		},
		Transport: helix.EventSubTransport{
			Method:    "websocket",
			SessionID: sessionID,
		},
	})
	hc.mu.Unlock()

	if err != nil {
		return "", fmt.Errorf("failed to create eventsub subscription: %w", err)
	}

	if resp.StatusCode != 202 {
		return "", fmt.Errorf("unexpected status code: %d, error: %s, message: %s", resp.StatusCode, resp.Error, resp.ErrorMessage)
	}

	if len(resp.Data.EventSubSubscriptions) == 0 {
		return "", fmt.Errorf("no subscription returned")
	}

	return resp.Data.EventSubSubscriptions[0].ID, nil
}

func (hc *HelixClient) DeleteEventSubSubscription(ctx context.Context, userID uuid.UUID, subscriptionID string) error {
	user, err := hc.tokenRefresher.EnsureValidToken(ctx, userID)
	if err != nil {
		return err
	}

	hc.mu.Lock()
	hc.client.SetUserAccessToken(user.AccessToken)
	resp, err := hc.client.RemoveEventSubSubscription(subscriptionID)
	hc.mu.Unlock()

	if err != nil {
		return fmt.Errorf("failed to delete eventsub subscription: %w", err)
	}

	if resp.StatusCode != 204 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
