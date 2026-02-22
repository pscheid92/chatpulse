package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/adapter/postgres/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
)

type EventSubRepo struct {
	q *sqlcgen.Queries
}

func NewEventSubRepo(pool *pgxpool.Pool) *EventSubRepo {
	return &EventSubRepo{q: sqlcgen.New(pool)}
}

func (r *EventSubRepo) Create(ctx context.Context, streamerID uuid.UUID, subscriptionID, conduitID string) error {
	params := sqlcgen.CreateEventSubSubscriptionParams{
		StreamerID:     streamerID,
		SubscriptionID: subscriptionID,
		ConduitID:      conduitID,
	}
	if err := r.q.CreateEventSubSubscription(ctx, params); err != nil {
		return fmt.Errorf("failed to create EventSub subscription: %w", err)
	}
	return nil
}

func (r *EventSubRepo) GetByStreamerID(ctx context.Context, streamerID uuid.UUID) (*domain.EventSubSubscription, error) {
	row, err := r.q.GetEventSubByStreamerID(ctx, streamerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get EventSub subscription by streamer ID: %w", err)
	}

	eventSub := domain.EventSubSubscription{
		StreamerID:        row.StreamerID,
		BroadcasterUserID: row.BroadcasterUserID,
		SubscriptionID:    row.SubscriptionID,
		ConduitID:         row.ConduitID,
		CreatedAt:         row.CreatedAt,
	}
	return &eventSub, nil
}

func (r *EventSubRepo) Delete(ctx context.Context, streamerID uuid.UUID) error {
	if err := r.q.DeleteEventSubByStreamerID(ctx, streamerID); err != nil {
		return fmt.Errorf("failed to delete EventSub subscription by streamer ID: %w", err)
	}
	return nil
}

func (r *EventSubRepo) DeleteByConduitID(ctx context.Context, conduitID string) error {
	if err := r.q.DeleteEventSubByConduitID(ctx, conduitID); err != nil {
		return fmt.Errorf("failed to delete EventSub subscriptions by conduit ID: %w", err)
	}
	return nil
}

func (r *EventSubRepo) List(ctx context.Context) ([]domain.EventSubSubscription, error) {
	rows, err := r.q.ListEventSubSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list EventSub subscriptions: %w", err)
	}
	subs := make([]domain.EventSubSubscription, len(rows))
	for i, row := range rows {
		subs[i] = domain.EventSubSubscription{
			StreamerID:        row.StreamerID,
			BroadcasterUserID: row.BroadcasterUserID,
			SubscriptionID:    row.SubscriptionID,
			ConduitID:         row.ConduitID,
			CreatedAt:         row.CreatedAt,
		}
	}
	return subs, nil
}
