package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tgtriage/internal/domain"
)

// ConnectionRepo implements domain.ConnectionRepository.
type ConnectionRepo struct{ db *sql.DB }

var _ domain.ConnectionRepository = (*ConnectionRepo)(nil)

func (r *ConnectionRepo) Upsert(ctx context.Context, c *domain.BusinessConnection) error {
	c.UpdatedAt = time.Now()
	_, err := r.db.ExecContext(ctx, `INSERT INTO business_connections
		(id, user_id, user_chat_id, user_name, can_reply, can_read_messages, is_enabled, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET
			user_id = excluded.user_id, user_chat_id = excluded.user_chat_id, user_name = excluded.user_name,
			can_reply = excluded.can_reply, can_read_messages = excluded.can_read_messages,
			is_enabled = excluded.is_enabled, updated_at = excluded.updated_at`,
		c.ID, c.UserID, c.UserChatID, c.UserName, boolInt(c.CanReply), boolInt(c.CanReadMessages), boolInt(c.Enabled),
		c.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert connection: %w", err)
	}
	return nil
}

func (r *ConnectionRepo) Get(ctx context.Context, id string) (*domain.BusinessConnection, error) {
	return r.one(ctx, `WHERE id = ?`, id)
}

func (r *ConnectionRepo) LatestForUser(ctx context.Context, userID int64) (*domain.BusinessConnection, error) {
	return r.one(ctx, `WHERE user_id = ? ORDER BY is_enabled DESC, updated_at DESC LIMIT 1`, userID)
}

func (r *ConnectionRepo) one(ctx context.Context, cond string, args ...any) (*domain.BusinessConnection, error) {
	var (
		c                         domain.BusinessConnection
		canReply, canRead, enable int
		updated                   int64
	)
	err := r.db.QueryRowContext(ctx, `SELECT id, user_id, user_chat_id, user_name, can_reply, can_read_messages,
		is_enabled, updated_at FROM business_connections `+cond, args...).
		Scan(&c.ID, &c.UserID, &c.UserChatID, &c.UserName, &canReply, &canRead, &enable, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	c.CanReply = canReply == 1
	c.CanReadMessages = canRead == 1
	c.Enabled = enable == 1
	c.UpdatedAt = fromUnix(updated)
	return &c, nil
}
