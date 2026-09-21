package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// HelpdeskRepo implements domain.HelpdeskRepository.
type HelpdeskRepo struct{ db *sql.DB }

var _ domain.HelpdeskRepository = (*HelpdeskRepo)(nil)

const hdUserColumns = `bot_id, user_id, name, username, language_code, source, group_id, topic_id, topic_closed, blocked, banned, banned_at, verified, hold, spam_flagged,
	awaiting_since, reminded_at, last_message_at, created_at, updated_at`

func scanHDUser(sc interface{ Scan(...any) error }) (domain.HelpdeskUser, error) {
	var (
		u                                           domain.HelpdeskUser
		closed, blocked, banned, verified, flagged  int
		bannedAt, awaiting, reminded, last, cr, upd int64
	)
	err := sc.Scan(&u.BotID, &u.UserID, &u.Name, &u.Username, &u.LanguageCode, &u.Source, &u.GroupID, &u.TopicID, &closed, &blocked, &banned, &bannedAt, &verified, &u.Hold, &flagged,
		&awaiting, &reminded, &last, &cr, &upd)
	if err != nil {
		return u, err
	}
	u.TopicClosed = closed == 1
	u.Blocked = blocked == 1
	u.Banned = banned == 1
	u.Verified = verified == 1
	u.SpamFlagged = flagged == 1
	u.BannedAt = ptrFromUnix(bannedAt)
	u.AwaitingSince = ptrFromUnix(awaiting)
	u.RemindedAt = ptrFromUnix(reminded)
	u.LastMessageAt = ptrFromUnix(last)
	u.CreatedAt = fromUnix(cr)
	u.UpdatedAt = fromUnix(upd)
	return u, nil
}

func (r *HelpdeskRepo) oneUser(ctx context.Context, cond string, args ...any) (*domain.HelpdeskUser, error) {
	u, err := scanHDUser(r.db.QueryRowContext(ctx, `SELECT `+hdUserColumns+` FROM hd_users `+cond, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get helpdesk user: %w", err)
	}
	return &u, nil
}

func (r *HelpdeskRepo) GetUser(ctx context.Context, botID, userID int64) (*domain.HelpdeskUser, error) {
	return r.oneUser(ctx, `WHERE bot_id = ? AND user_id = ?`, botID, userID)
}

func (r *HelpdeskRepo) UserByTopic(ctx context.Context, botID, groupID int64, topicID int) (*domain.HelpdeskUser, error) {
	return r.oneUser(ctx, `WHERE bot_id = ? AND group_id = ? AND topic_id = ? ORDER BY updated_at DESC LIMIT 1`,
		botID, groupID, topicID)
}

func (r *HelpdeskRepo) SaveUser(ctx context.Context, u *domain.HelpdeskUser) error {
	now := time.Now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `INSERT INTO hd_users (`+hdUserColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (bot_id, user_id) DO UPDATE SET
			name = excluded.name, username = excluded.username, language_code = excluded.language_code,
			source = excluded.source, group_id = excluded.group_id, topic_id = excluded.topic_id,
			topic_closed = excluded.topic_closed, blocked = excluded.blocked, banned = excluded.banned,
			banned_at = excluded.banned_at, verified = excluded.verified, hold = excluded.hold, spam_flagged = excluded.spam_flagged,
			awaiting_since = excluded.awaiting_since,
			reminded_at = excluded.reminded_at, last_message_at = excluded.last_message_at, updated_at = excluded.updated_at`,
		u.BotID, u.UserID, u.Name, u.Username, u.LanguageCode, u.Source, u.GroupID, u.TopicID, boolInt(u.TopicClosed), boolInt(u.Blocked), boolInt(u.Banned), ptrToUnix(u.BannedAt), boolInt(u.Verified), u.Hold, boolInt(u.SpamFlagged),
		ptrToUnix(u.AwaitingSince), ptrToUnix(u.RemindedAt), ptrToUnix(u.LastMessageAt), toUnix(u.CreatedAt), toUnix(u.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save helpdesk user: %w", err)
	}
	return nil
}

func (r *HelpdeskRepo) ListUsers(ctx context.Context, botID *int64, f domain.HelpdeskUserFilter) ([]domain.HelpdeskUser, int, error) {
	var (
		where []string
		args  []any
	)
	if botID != nil {
		where = append(where, "bot_id = ?")
		args = append(args, *botID)
	}
	if f.BannedOnly {
		where = append(where, "banned = 1")
	} else {
		where = append(where, "banned = 0")
	}
	if f.AwaitingOnly {
		where = append(where, "awaiting_since > 0")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, "(name LIKE ? OR username LIKE ? OR CAST(user_id AS TEXT) = ?)")
		like := "%" + strings.NewReplacer("%", "", "_", "").Replace(q) + "%"
		args = append(args, like, like, strings.TrimPrefix(q, "@"))
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hd_users`+cond, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count helpdesk users: %w", err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	order := "CASE WHEN awaiting_since = 0 THEN 1 ELSE 0 END, awaiting_since ASC, last_message_at DESC"
	if f.BannedOnly {
		order = "banned_at DESC"
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+hdUserColumns+` FROM hd_users`+cond+`
		ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list helpdesk users: %w", err)
	}
	defer rows.Close()
	var out []domain.HelpdeskUser
	for rows.Next() {
		u, err := scanHDUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (r *HelpdeskRepo) DueReminders(ctx context.Context, botID int64, cutoff time.Time) ([]domain.HelpdeskUser, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+hdUserColumns+` FROM hd_users
		WHERE bot_id = ? AND awaiting_since > 0 AND awaiting_since <= ? AND reminded_at <= ? AND topic_id > 0
			AND topic_closed = 0 AND blocked = 0 AND banned = 0
		ORDER BY awaiting_since`, botID, cutoff.Unix(), cutoff.Unix())
	if err != nil {
		return nil, fmt.Errorf("due reminders: %w", err)
	}
	defer rows.Close()
	var out []domain.HelpdeskUser
	for rows.Next() {
		u, err := scanHDUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

const hdMessageColumns = `id, bot_id, user_id, direction, user_msg_id, group_id, group_msg_id, operator_id, created_at`

func scanHDMessage(sc interface{ Scan(...any) error }) (domain.HelpdeskMessage, error) {
	var (
		m   domain.HelpdeskMessage
		dir string
		cr  int64
	)
	if err := sc.Scan(&m.ID, &m.BotID, &m.UserID, &dir, &m.UserMsgID, &m.GroupID, &m.GroupMsgID, &m.OperatorID, &cr); err != nil {
		return m, err
	}
	m.Direction = domain.HelpdeskDirection(dir)
	m.CreatedAt = fromUnix(cr)
	return m, nil
}

func (r *HelpdeskRepo) SaveMessage(ctx context.Context, m *domain.HelpdeskMessage) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO hd_messages
		(bot_id, user_id, direction, user_msg_id, group_id, group_msg_id, operator_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		m.BotID, m.UserID, string(m.Direction), m.UserMsgID, m.GroupID, m.GroupMsgID, m.OperatorID, m.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("save helpdesk message: %w", err)
	}
	m.ID, err = res.LastInsertId()
	return err
}

func (r *HelpdeskRepo) oneMessage(ctx context.Context, cond string, args ...any) (*domain.HelpdeskMessage, error) {
	m, err := scanHDMessage(r.db.QueryRowContext(ctx, `SELECT `+hdMessageColumns+` FROM hd_messages `+cond+` ORDER BY id DESC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get helpdesk message: %w", err)
	}
	return &m, nil
}

func (r *HelpdeskRepo) MessageByUserMsg(ctx context.Context, botID, userID int64, userMsgID int) (*domain.HelpdeskMessage, error) {
	return r.oneMessage(ctx, `WHERE bot_id = ? AND user_id = ? AND user_msg_id = ?`, botID, userID, userMsgID)
}

func (r *HelpdeskRepo) MessageByGroupMsg(ctx context.Context, botID, groupID int64, groupMsgID int) (*domain.HelpdeskMessage, error) {
	return r.oneMessage(ctx, `WHERE bot_id = ? AND group_id = ? AND group_msg_id = ?`, botID, groupID, groupMsgID)
}

func (r *HelpdeskRepo) MessagesAround(ctx context.Context, botID, groupID int64, t time.Time, window time.Duration) ([]domain.HelpdeskMessage, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+hdMessageColumns+` FROM hd_messages
		WHERE bot_id = ? AND group_id = ? AND direction = ? AND created_at BETWEEN ? AND ? ORDER BY id`,
		botID, groupID, string(domain.HelpdeskIn), t.Add(-window).Unix(), t.Add(window).Unix())
	if err != nil {
		return nil, fmt.Errorf("helpdesk messages around: %w", err)
	}
	defer rows.Close()
	var out []domain.HelpdeskMessage
	for rows.Next() {
		m, err := scanHDMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *HelpdeskRepo) DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM hd_messages WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *HelpdeskRepo) SaveCard(ctx context.Context, c domain.HelpdeskCard) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO hd_cards (task_id, chat_id, topic_id, message_id) VALUES (?,?,?,?)
		ON CONFLICT DO NOTHING`, c.TaskID, c.ChatID, c.TopicID, c.MessageID)
	return err
}

func (r *HelpdeskRepo) Cards(ctx context.Context, taskID int64) ([]domain.HelpdeskCard, error) {
	return r.cards(ctx, `WHERE task_id = ?`, taskID)
}

func (r *HelpdeskRepo) CardsInTopic(ctx context.Context, chatID int64, topicID int) ([]domain.HelpdeskCard, error) {
	return r.cards(ctx, `WHERE chat_id = ? AND topic_id = ?`, chatID, topicID)
}

func (r *HelpdeskRepo) DeleteCard(ctx context.Context, c domain.HelpdeskCard) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM hd_cards WHERE task_id = ? AND chat_id = ? AND message_id = ?`, c.TaskID, c.ChatID, c.MessageID)
	return err
}

func (r *HelpdeskRepo) cards(ctx context.Context, cond string, args ...any) ([]domain.HelpdeskCard, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT task_id, chat_id, topic_id, message_id FROM hd_cards `+cond, args...)
	if err != nil {
		return nil, fmt.Errorf("helpdesk cards: %w", err)
	}
	defer rows.Close()
	var out []domain.HelpdeskCard
	for rows.Next() {
		var c domain.HelpdeskCard
		if err := rows.Scan(&c.TaskID, &c.ChatID, &c.TopicID, &c.MessageID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *HelpdeskRepo) AddHeld(ctx context.Context, m *domain.HeldMessage) error {
	res, err := r.db.ExecContext(ctx, `INSERT INTO hd_held (bot_id, user_id, message_id, media_group_id, reply_to_id, text, sent_at, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, m.BotID, m.UserID, m.MessageID, m.MediaGroupID, m.ReplyToID, m.Text, toUnix(m.SentAt), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("add held message: %w", err)
	}
	m.ID, err = res.LastInsertId()
	return err
}

func (r *HelpdeskRepo) HeldMessages(ctx context.Context, botID, userID int64) ([]domain.HeldMessage, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, bot_id, user_id, message_id, media_group_id, reply_to_id, text, sent_at
		FROM hd_held WHERE bot_id = ? AND user_id = ? ORDER BY id`, botID, userID)
	if err != nil {
		return nil, fmt.Errorf("held messages: %w", err)
	}
	defer rows.Close()
	var out []domain.HeldMessage
	for rows.Next() {
		var (
			m    domain.HeldMessage
			sent int64
		)
		if err := rows.Scan(&m.ID, &m.BotID, &m.UserID, &m.MessageID, &m.MediaGroupID, &m.ReplyToID, &m.Text, &sent); err != nil {
			return nil, err
		}
		m.SentAt = fromUnix(sent)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *HelpdeskRepo) DeleteHeld(ctx context.Context, botID, userID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM hd_held WHERE bot_id = ? AND user_id = ?`, botID, userID)
	return err
}

func (r *HelpdeskRepo) DeleteHeldOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM hd_held WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
