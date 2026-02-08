package twitch

import (
	"context"
	"fmt"
	"log"

	"github.com/Its-donkey/kappopher/helix"
)

const defaultShardID = "0"

// conduitAPIClient is the subset of TwitchClient used by ConduitManager.
type conduitAPIClient interface {
	CreateConduit(ctx context.Context, shardCount int) (*helix.Conduit, error)
	GetConduits(ctx context.Context) ([]helix.Conduit, error)
	UpdateConduitShards(ctx context.Context, params *helix.UpdateConduitShardsParams) (*helix.UpdateConduitShardsResponse, error)
	DeleteConduit(ctx context.Context, conduitID string) error
}

// ConduitManager manages the lifecycle of a Twitch EventSub conduit
// with a webhook shard pointing at this server.
type ConduitManager struct {
	client      conduitAPIClient
	conduitID   string
	callbackURL string
	secret      string
}

// NewConduitManager creates a new ConduitManager.
func NewConduitManager(client conduitAPIClient, callbackURL, secret string) *ConduitManager {
	return &ConduitManager{
		client:      client,
		callbackURL: callbackURL,
		secret:      secret,
	}
}

// Setup creates or finds an existing conduit, then configures its shard
// with a webhook transport pointing at callbackURL. Returns the conduit ID.
func (cm *ConduitManager) Setup(ctx context.Context) (string, error) {
	// Check for existing conduit first (idempotent restart)
	conduits, err := cm.client.GetConduits(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list conduits: %w", err)
	}

	var conduit *helix.Conduit
	if len(conduits) > 0 {
		conduit = &conduits[0]
		log.Printf("Found existing conduit %s", conduit.ID)
	} else {
		conduit, err = cm.client.CreateConduit(ctx, 1)
		if err != nil {
			return "", fmt.Errorf("failed to create conduit: %w", err)
		}
	}

	cm.conduitID = conduit.ID

	// Update shard 0 with webhook transport.
	// Shard IDs are zero-indexed strings; we always create conduits with 1 shard.
	err = cm.configureShard(ctx, conduit.ID)
	if err != nil {
		// Stale conduit from a previous crash — delete and create fresh
		log.Printf("Shard configuration failed for conduit %s, recreating: %v", conduit.ID, err)
		if delErr := cm.client.DeleteConduit(ctx, conduit.ID); delErr != nil {
			return "", fmt.Errorf("failed to delete stale conduit: %w", delErr)
		}
		conduit, err = cm.client.CreateConduit(ctx, 1)
		if err != nil {
			return "", fmt.Errorf("failed to recreate conduit: %w", err)
		}
		cm.conduitID = conduit.ID
		if err = cm.configureShard(ctx, conduit.ID); err != nil {
			return "", fmt.Errorf("failed to configure shard on new conduit: %w", err)
		}
	}

	log.Printf("Conduit %s configured with webhook shard pointing to %s", conduit.ID, cm.callbackURL)
	return conduit.ID, nil
}

func (cm *ConduitManager) configureShard(ctx context.Context, conduitID string) error {
	_, err := cm.client.UpdateConduitShards(ctx, &helix.UpdateConduitShardsParams{
		ConduitID: conduitID,
		Shards: []helix.UpdateConduitShardParams{
			{
				ID: defaultShardID,
				Transport: helix.UpdateConduitShardTransport{
					Method:   "webhook",
					Callback: cm.callbackURL,
					Secret:   cm.secret,
				},
			},
		},
	})
	return err
}

// ConduitID returns the current conduit ID. Empty if Setup hasn't been called.
func (cm *ConduitManager) ConduitID() string {
	return cm.conduitID
}

// Cleanup deletes the conduit. Call on graceful shutdown.
func (cm *ConduitManager) Cleanup(ctx context.Context) error {
	if cm.conduitID == "" {
		return nil
	}
	return cm.client.DeleteConduit(ctx, cm.conduitID)
}
