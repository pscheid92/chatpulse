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

type ConfigRepo struct {
	q *sqlcgen.Queries
}

func NewConfigRepo(pool *pgxpool.Pool) *ConfigRepo {
	return &ConfigRepo{q: sqlcgen.New(pool)}
}

func (r *ConfigRepo) GetByStreamerID(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
	row, err := r.q.GetConfigByStreamerID(ctx, streamerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get config by streamer ID: %w", err)
	}

	overlayConfig := domain.OverlayConfig{
		MemorySeconds:  int(row.MemorySeconds),
		ForTrigger:     row.ForTrigger,
		ForLabel:       row.ForLabel,
		AgainstTrigger: row.AgainstTrigger,
		AgainstLabel:   row.AgainstLabel,
		DisplayMode:    domain.ParseDisplayMode(row.DisplayMode),
		Theme:          domain.ParseTheme(row.Theme),
	}

	overlayConfigWithVersion := domain.OverlayConfigWithVersion{
		OverlayConfig: overlayConfig,
		Version:       int(row.Version),
	}

	return &overlayConfigWithVersion, nil
}

func (r *ConfigRepo) GetByBroadcasterID(ctx context.Context, broadcasterID string) (*domain.OverlayConfigWithVersion, error) {
	row, err := r.q.GetConfigByBroadcasterID(ctx, broadcasterID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get config by broadcaster ID: %w", err)
	}

	overlayConfig := domain.OverlayConfig{
		MemorySeconds:  int(row.MemorySeconds),
		ForTrigger:     row.ForTrigger,
		ForLabel:       row.ForLabel,
		AgainstTrigger: row.AgainstTrigger,
		AgainstLabel:   row.AgainstLabel,
		DisplayMode:    domain.ParseDisplayMode(row.DisplayMode),
		Theme:          domain.ParseTheme(row.Theme),
	}

	overlayConfigWithVersion := domain.OverlayConfigWithVersion{
		OverlayConfig: overlayConfig,
		Version:       int(row.Version),
	}

	return &overlayConfigWithVersion, nil
}

func (r *ConfigRepo) Update(ctx context.Context, streamerID uuid.UUID, config domain.OverlayConfig, version int) error {
	tag, err := r.q.UpdateConfig(ctx, sqlcgen.UpdateConfigParams{
		ForTrigger:     config.ForTrigger,
		AgainstTrigger: config.AgainstTrigger,
		AgainstLabel:   config.AgainstLabel,
		ForLabel:       config.ForLabel,
		MemorySeconds:  int32(config.MemorySeconds),
		DisplayMode:    string(config.DisplayMode),
		Theme:          string(config.Theme),
		Version:        int32(version),
		StreamerID:     streamerID,
	})
	if err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConfigNotFound
	}
	return nil
}
