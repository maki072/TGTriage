package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"tgtriage/internal/domain"
)

// MessageRepo implements domain.MessageRepository.
type MessageRepo struct{ db *sql.DB }

var _ domain.MessageRepository = (*MessageRepo)(nil)

const messageColumns = `id, connection_id, chat_id, message_id, sender_id, sender_name, sender_username,
	outgoing, text, sent_at, analyzed, analysis_id, deleted`

func scanMessage(sc interface{ Scan(...any) error }) (domain.Message, error) {
	var (
		m                           domain.Message
		outgoing, analyzed, deleted int
		sentAt                      int64
	)
	err := sc.Scan(&m.ID, &m.ConnectionID, &m.ChatID, &m.MessageID, &m.SenderID, &m.SenderName, &m.SenderUsername,
		&outgoing, &m.Text, &sentAt, &analyzed, &m.AnalysisID, &deleted)
	if err != nil {
		return m, err
	}
	m.Outgoing = outgoing == 1
	m.Analyzed = analyzed == 1
	m.Deleted = deleted == 1
	m.SentAt = fromUnix(sentAt)
	return m, nil
}

func (r *MessageRepo) Save(ctx context.Context, m *domain.Message) (bool, error) {
	res, err := r.db.ExecContext(ctx, `INSERT INTO messages
		(connection_id, chat_id, message_id, sender_id, sender_name, sender_username, outgoing, text, sent_at, analyzed, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (connection_id, chat_id, message_id) DO NOTHING`,
		m.ConnectionID, m.ChatID, m.MessageID, m.SenderID, m.SenderName, m.SenderUsername,
		boolInt(m.Outgoing), m.Text, toUnix(m.SentAt), boolInt(m.Analyzed), time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("insert message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return false, err
	}
	m.ID = id
	return true, nil
}

func (r *MessageRepo) UpdateText(ctx context.Context, connectionID string, chatID int64, messageID int, text string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE messages SET text = ? WHERE connection_id = ? AND chat_id = ? AND message_id = ?`,
		text, connectionID, chatID, messageID)
	return err
}

func (r *MessageRepo) Find(ctx context.Context, connectionID string, chatID int64, messageID int) (*domain.Message, error) {
	msgs, err := r.query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE connection_id = ? AND chat_id = ? AND message_id = ? LIMIT 1`, connectionID, chatID, messageID)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, domain.ErrNotFound
	}
	return &msgs[0], nil
}

func (r *MessageRepo) MarkDeleted(ctx context.Context, connectionID string, chatID int64, messageIDs []int) error {
	if len(messageIDs) == 0 {
		return nil
	}
	args := []any{connectionID, chatID}
	for _, id := range messageIDs {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE messages SET deleted = 1
		WHERE connection_id = ? AND chat_id = ? AND message_id IN (`+placeholders(len(messageIDs))+`)`, args...)
	return err
}

func (r *MessageRepo) GetByIDs(ctx context.Context, ids []int64) ([]domain.Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return r.query(ctx, `SELECT `+messageColumns+` FROM messages WHERE id IN (`+placeholders(len(ids))+`) ORDER BY id`, args...)
}

func (r *MessageRepo) History(ctx context.Context, connectionID string, chatID int64, beforeID int64, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	msgs, err := r.query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE connection_id = ? AND chat_id = ? AND id < ? AND deleted = 0
		ORDER BY id DESC LIMIT ?`, connectionID, chatID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (r *MessageRepo) Pending(ctx context.Context) ([]domain.Message, error) {
	return r.query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE analyzed = 0 AND outgoing = 0 AND deleted = 0 ORDER BY id`)
}

func (r *MessageRepo) MarkAnalyzed(ctx context.Context, ids []int64, analysisID int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{analysisID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE messages SET analyzed = 1, analysis_id = ?
		WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

func (r *MessageRepo) DeleteOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM messages WHERE sent_at < ? AND analyzed = 1`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *MessageRepo) query(ctx context.Context, q string, args ...any) ([]domain.Message, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
