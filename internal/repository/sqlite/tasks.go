package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// TaskRepo implements domain.TaskRepository.
type TaskRepo struct{ db *sql.DB }

var _ domain.TaskRepository = (*TaskRepo)(nil)

const taskColumns = `id, connection_id, chat_id, sender_id, sender_name, sender_username, source_message_ids, source_text,
	title, description, priority, category, deadline, draft_reply, reply_strategy, confidence, status, prev_status,
	snooze_until, analysis_id, provider, model, reply_sent_at, reply_text, importance, remind_at, last_reminded_at,
	merged_into, created_at, updated_at, closed_at`

func scanTask(sc interface{ Scan(...any) error }) (domain.Task, error) {
	var (
		t                                         domain.Task
		srcIDs, priority, category, status, prev  string
		importance                                string
		deadline, snooze, replySent, created, upd int64
		closed, remindAt, lastReminded            int64
	)
	err := sc.Scan(&t.ID, &t.ConnectionID, &t.ChatID, &t.SenderID, &t.SenderName, &t.SenderUsername, &srcIDs, &t.SourceText,
		&t.Title, &t.Description, &priority, &category, &deadline, &t.DraftReply, &t.ReplyStrategy, &t.Confidence, &status, &prev,
		&snooze, &t.AnalysisID, &t.Provider, &t.Model, &replySent, &t.ReplyText, &importance, &remindAt, &lastReminded,
		&t.MergedInto, &created, &upd, &closed)
	if err != nil {
		return t, err
	}
	if srcIDs != "" {
		_ = json.Unmarshal([]byte(srcIDs), &t.SourceMessageIDs)
	}
	t.Priority = domain.ParsePriority(priority)
	t.Category = domain.ParseCategory(category)
	t.Status = domain.TaskStatus(status)
	t.PrevStatus = domain.TaskStatus(prev)
	t.Deadline = ptrFromUnix(deadline)
	t.SnoozeUntil = ptrFromUnix(snooze)
	t.ReplySentAt = ptrFromUnix(replySent)
	t.Importance = domain.ParsePriority(importance)
	t.RemindAt = ptrFromUnix(remindAt)
	t.LastRemindedAt = ptrFromUnix(lastReminded)
	t.CreatedAt = fromUnix(created)
	t.UpdatedAt = fromUnix(upd)
	t.ClosedAt = ptrFromUnix(closed)
	return t, nil
}

func (r *TaskRepo) Create(ctx context.Context, t *domain.Task) error {
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	src, _ := json.Marshal(t.SourceMessageIDs)
	res, err := r.db.ExecContext(ctx, `INSERT INTO tasks
		(connection_id, chat_id, sender_id, sender_name, sender_username, source_message_ids, source_text,
		 title, description, priority, priority_rank, category, deadline, draft_reply, reply_strategy, confidence,
		 status, prev_status, snooze_until, analysis_id, provider, model, reply_sent_at, reply_text,
		 importance, remind_at, last_reminded_at, merged_into,
		 created_at, updated_at, closed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ConnectionID, t.ChatID, t.SenderID, t.SenderName, t.SenderUsername, string(src), t.SourceText,
		t.Title, t.Description, string(t.Priority), t.Priority.Rank(), string(t.Category), ptrToUnix(t.Deadline),
		t.DraftReply, t.ReplyStrategy, t.Confidence, string(t.Status), string(t.PrevStatus), ptrToUnix(t.SnoozeUntil),
		t.AnalysisID, t.Provider, t.Model, ptrToUnix(t.ReplySentAt), t.ReplyText,
		string(t.Importance), ptrToUnix(t.RemindAt), ptrToUnix(t.LastRemindedAt), t.MergedInto,
		toUnix(t.CreatedAt), toUnix(t.UpdatedAt), ptrToUnix(t.ClosedAt))
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	t.ID = id
	return nil
}

func (r *TaskRepo) Update(ctx context.Context, t *domain.Task) error {
	t.UpdatedAt = time.Now()
	src, _ := json.Marshal(t.SourceMessageIDs)
	res, err := r.db.ExecContext(ctx, `UPDATE tasks SET
		sender_name = ?, sender_username = ?, source_message_ids = ?, source_text = ?, title = ?, description = ?,
		priority = ?, priority_rank = ?, category = ?, deadline = ?, draft_reply = ?, reply_strategy = ?, confidence = ?,
		status = ?, prev_status = ?, snooze_until = ?, reply_sent_at = ?, reply_text = ?,
		importance = ?, remind_at = ?, last_reminded_at = ?, merged_into = ?, updated_at = ?, closed_at = ?
		WHERE id = ?`,
		t.SenderName, t.SenderUsername, string(src), t.SourceText, t.Title, t.Description,
		string(t.Priority), t.Priority.Rank(), string(t.Category), ptrToUnix(t.Deadline), t.DraftReply, t.ReplyStrategy, t.Confidence,
		string(t.Status), string(t.PrevStatus), ptrToUnix(t.SnoozeUntil), ptrToUnix(t.ReplySentAt), t.ReplyText,
		string(t.Importance), ptrToUnix(t.RemindAt), ptrToUnix(t.LastRemindedAt), t.MergedInto,
		toUnix(t.UpdatedAt), ptrToUnix(t.ClosedAt), t.ID)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *TaskRepo) Get(ctx context.Context, id int64) (*domain.Task, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return &t, nil
}

func (r *TaskRepo) List(ctx context.Context, f domain.TaskFilter) ([]domain.Task, int, error) {
	var (
		where []string
		args  []any
	)
	if len(f.Statuses) > 0 {
		where = append(where, "status IN ("+placeholders(len(f.Statuses))+")")
		for _, s := range f.Statuses {
			args = append(args, string(s))
		}
	}
	if len(f.Priorities) > 0 {
		where = append(where, "priority IN ("+placeholders(len(f.Priorities))+")")
		for _, p := range f.Priorities {
			args = append(args, string(p))
		}
	}
	if c, a := scopeCond(f.Scope); c != "" {
		where = append(where, c)
		args = append(args, a...)
	}
	if f.ChatID != 0 {
		where = append(where, "chat_id = ?")
		args = append(args, f.ChatID)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`+cond, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}

	order := ` ORDER BY priority_rank ASC, CASE WHEN deadline = 0 THEN 1 ELSE 0 END, deadline ASC, id DESC`
	if onlyClosed(f.Statuses) {
		order = ` ORDER BY closed_at DESC, id DESC`
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + taskColumns + ` FROM tasks` + cond + order + ` LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func onlyClosed(statuses []domain.TaskStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s.IsOpen() {
			return false
		}
	}
	return true
}

func (r *TaskRepo) DueSnoozed(ctx context.Context, now time.Time) ([]domain.Task, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE status = ? AND snooze_until > 0 AND snooze_until <= ? ORDER BY snooze_until`,
		string(domain.StatusSnoozed), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("due snoozed: %w", err)
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TaskRepo) DueRemind(ctx context.Context, now time.Time) ([]domain.Task, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE remind_at > 0 AND remind_at <= ? AND status IN (?, ?, ?) ORDER BY remind_at`,
		now.Unix(), string(domain.StatusNew), string(domain.StatusInProgress), string(domain.StatusSnoozed))
	if err != nil {
		return nil, fmt.Errorf("due remind: %w", err)
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TaskRepo) DuePersonalNudge(ctx context.Context, cutoff time.Time) ([]domain.Task, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE status IN (?, ?) AND connection_id <> ? AND chat_id <> 0 AND merged_into = 0
			AND (CASE WHEN last_reminded_at > 0 THEN last_reminded_at ELSE created_at END) <= ?
		ORDER BY created_at`,
		string(domain.StatusNew), string(domain.StatusInProgress), domain.HelpdeskConnectionID, cutoff.Unix())
	if err != nil {
		return nil, fmt.Errorf("due personal nudge: %w", err)
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// scopeCond returns the SQL condition (with args) selecting tasks of a scope; empty for all tasks.
func scopeCond(scope domain.TaskScope) (string, []any) {
	switch scope {
	case domain.ScopeHelpdesk:
		return "connection_id = ?", []any{domain.HelpdeskConnectionID}
	case domain.ScopePersonal:
		return "connection_id <> ?", []any{domain.HelpdeskConnectionID}
	}
	return "", nil
}

func andScope(scope domain.TaskScope, args []any) (string, []any) {
	c, a := scopeCond(scope)
	if c == "" {
		return "", args
	}
	return " AND " + c, append(args, a...)
}

func (r *TaskRepo) CountByStatus(ctx context.Context, scope domain.TaskScope, since time.Time) (map[domain.TaskStatus]int, error) {
	cond, args := andScope(scope, []any{toUnix(since)})
	rows, err := r.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM tasks WHERE created_at >= ?`+cond+` GROUP BY status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count by status: %w", err)
	}
	defer rows.Close()
	out := make(map[domain.TaskStatus]int)
	for rows.Next() {
		var (
			s string
			n int
		)
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[domain.TaskStatus(s)] = n
	}
	return out, rows.Err()
}

func (r *TaskRepo) CountOverdue(ctx context.Context, scope domain.TaskScope, now time.Time) (int, error) {
	var n int
	cond, args := andScope(scope, []any{string(domain.StatusNew), string(domain.StatusInProgress), now.Unix()})
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks
		WHERE status IN (?, ?) AND deadline > 0 AND deadline < ?`+cond, args...).Scan(&n)
	return n, err
}
