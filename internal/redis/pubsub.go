package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// SentimentUpdate is the message published via Redis Pub/Sub.
type SentimentUpdate struct {
	Value  float64 `json:"value"`
	Status string  `json:"status"`
}

func sentimentChannel(sessionUUID uuid.UUID) string {
	return "sentiment:" + sessionUUID.String()
}

// PubSub provides cross-instance broadcast via Redis Pub/Sub.
type PubSub struct {
	rdb *goredis.Client
}

// NewPubSub creates a new PubSub instance.
func NewPubSub(client *Client) *PubSub {
	return &PubSub{rdb: client.rdb}
}

// PublishUpdate publishes a sentiment update to the channel for a session.
func (ps *PubSub) PublishUpdate(ctx context.Context, sessionUUID uuid.UUID, value float64, status string) error {
	msg := SentimentUpdate{Value: value, Status: status}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal update: %w", err)
	}
	return ps.rdb.Publish(ctx, sentimentChannel(sessionUUID), data).Err()
}

// Subscription represents an active Pub/Sub subscription for a session.
type Subscription struct {
	sub    *goredis.PubSub
	Ch     <-chan SentimentUpdate
	cancel context.CancelFunc
}

// Close unsubscribes and closes the subscription.
func (s *Subscription) Close() {
	s.cancel()
	_ = s.sub.Close()
}

// SubscribeSession subscribes to sentiment updates for a session.
// Returns a Subscription with a channel that receives updates.
// Call subscription.Close() when done.
func (ps *PubSub) SubscribeSession(ctx context.Context, sessionUUID uuid.UUID) *Subscription {
	channel := sentimentChannel(sessionUUID)
	sub := ps.rdb.Subscribe(ctx, channel)

	subCtx, cancel := context.WithCancel(ctx)
	ch := make(chan SentimentUpdate, 16)

	go func() {
		defer close(ch)
		msgCh := sub.Channel()
		for {
			select {
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var update SentimentUpdate
				if err := json.Unmarshal([]byte(msg.Payload), &update); err != nil {
					log.Printf("Failed to unmarshal pubsub message: %v", err)
					continue
				}
				select {
				case ch <- update:
				default:
					// Drop if receiver is slow
				}
			case <-subCtx.Done():
				return
			}
		}
	}()

	return &Subscription{
		sub:    sub,
		Ch:     ch,
		cancel: cancel,
	}
}
