package service

import (
	"context"
	"errors"

	"tgtriage/internal/domain"
)

// ConnectionService manages the owner's Telegram Business connection.
type ConnectionService struct {
	repo    domain.ConnectionRepository
	ownerID int64
}

func NewConnectionService(repo domain.ConnectionRepository, ownerID int64) *ConnectionService {
	return &ConnectionService{repo: repo, ownerID: ownerID}
}

// IsOwner reports whether the connection belongs to the configured owner.
func (s *ConnectionService) IsOwner(c *domain.BusinessConnection) bool {
	return c != nil && c.UserID == s.ownerID
}

func (s *ConnectionService) Save(ctx context.Context, c *domain.BusinessConnection) error {
	if !s.IsOwner(c) {
		return domain.ErrInvalidInput
	}
	return s.repo.Upsert(ctx, c)
}

func (s *ConnectionService) Get(ctx context.Context, id string) (*domain.BusinessConnection, error) {
	return s.repo.Get(ctx, id)
}

// Current returns the most relevant connection of the owner.
func (s *ConnectionService) Current(ctx context.Context) (*domain.BusinessConnection, error) {
	return s.repo.LatestForUser(ctx, s.ownerID)
}

// Resolve returns an enabled connection: preferred id first, then the latest one of the owner.
// The connection id changes when the owner reconnects the bot, so old tasks keep working.
func (s *ConnectionService) Resolve(ctx context.Context, preferredID string) (*domain.BusinessConnection, error) {
	if preferredID != "" {
		c, err := s.repo.Get(ctx, preferredID)
		if err == nil && c.Enabled && s.IsOwner(c) {
			return c, nil
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	c, err := s.Current(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.ErrNoConnection
	}
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, domain.ErrNoConnection
	}
	return c, nil
}
