package database

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// EventSubRepo implements domain.EventSubRepository backed by PostgreSQL.
type EventSubRepo struct {
	db *sql.DB
}

// NewEventSubRepo creates an EventSubRepo from the shared DB connection.
func NewEventSubRepo(db *DB) *EventSubRepo {
	return &EventSubRepo{db: db.DB}
}

func (r *EventSubRepo) Create(ctx context.Context, userID uuid.UUID, broadcasterUserID, subscriptionID, conduitID string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO eventsub_subscriptions (user_id, broadcaster_user_id, subscription_id, conduit_id, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			broadcaster_user_id = EXCLUDED.broadcaster_user_id,
			subscription_id = EXCLUDED.subscription_id,
			conduit_id = EXCLUDED.conduit_id
	`, userID, broadcasterUserID, subscriptionID, conduitID)
	return err
}

func (r *EventSubRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.EventSubSubscription, error) {
	var sub domain.EventSubSubscription
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, broadcaster_user_id, subscription_id, conduit_id, created_at
		FROM eventsub_subscriptions
		WHERE user_id = $1
	`, userID).Scan(&sub.UserID, &sub.BroadcasterUserID, &sub.SubscriptionID, &sub.ConduitID, &sub.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func (r *EventSubRepo) Delete(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM eventsub_subscriptions WHERE user_id = $1`, userID)
	return err
}

func (r *EventSubRepo) List(ctx context.Context) ([]domain.EventSubSubscription, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, broadcaster_user_id, subscription_id, conduit_id, created_at
		FROM eventsub_subscriptions
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []domain.EventSubSubscription
	for rows.Next() {
		var sub domain.EventSubSubscription
		if err := rows.Scan(&sub.UserID, &sub.BroadcasterUserID, &sub.SubscriptionID, &sub.ConduitID, &sub.CreatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}
