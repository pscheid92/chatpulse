package database

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pscheid92/chatpulse/internal/database/sqlcgen"
	"github.com/pscheid92/chatpulse/internal/domain"
)

type EventSubRepo struct {
	q *sqlcgen.Queries
}

func NewEventSubRepo(pool *pgxpool.Pool) *EventSubRepo {
	return &EventSubRepo{q: sqlcgen.New(pool)}
}

func (r *EventSubRepo) Create(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error {
	return r.q.CreateEventSubSubscription(ctx, sqlcgen.CreateEventSubSubscriptionParams{
		UserID:            userID,
		BroadcasterUserID: broadcasterUserID,
		SubscriptionID:    subscriptionID,
		ConduitID:         conduitID,
	})
}

func (r *EventSubRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.EventSubSubscription, error) {
	row, err := r.q.GetEventSubByUserID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &domain.EventSubSubscription{
		UserID:            row.UserID,
		BroadcasterUserID: row.BroadcasterUserID,
		SubscriptionID:    row.SubscriptionID,
		ConduitID:         row.ConduitID,
		CreatedAt:         row.CreatedAt,
	}, nil
}

func (r *EventSubRepo) Delete(ctx context.Context, userID uuid.UUID) error {
	return r.q.DeleteEventSubByUserID(ctx, userID)
}

func (r *EventSubRepo) List(ctx context.Context) ([]domain.EventSubSubscription, error) {
	rows, err := r.q.ListEventSubSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	subs := make([]domain.EventSubSubscription, len(rows))
	for i, row := range rows {
		subs[i] = domain.EventSubSubscription{
			UserID:            row.UserID,
			BroadcasterUserID: row.BroadcasterUserID,
			SubscriptionID:    row.SubscriptionID,
			ConduitID:         row.ConduitID,
			CreatedAt:         row.CreatedAt,
		}
	}
	return subs, nil
}
