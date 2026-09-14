package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"tgtriage/internal/domain"
)

// AnalysisRepo implements domain.AnalysisRepository.
type AnalysisRepo struct{ db *sql.DB }

var _ domain.AnalysisRepository = (*AnalysisRepo)(nil)

func (r *AnalysisRepo) Create(ctx context.Context, a *domain.AnalysisRecord) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	ids, _ := json.Marshal(a.MessageRowIDs)
	res, err := r.db.ExecContext(ctx, `INSERT INTO analyses
		(connection_id, chat_id, message_ids, input_text, provider, model, raw_response, is_task, confidence,
		 message_type, status, error, task_id, latency_ms, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ConnectionID, a.ChatID, string(ids), a.InputText, a.Provider, a.Model, a.RawResponse, boolInt(a.IsTask),
		a.Confidence, a.MessageType, string(a.Status), a.Error, a.TaskID, a.LatencyMs, toUnix(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert analysis: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	a.ID = id
	return nil
}

func (r *AnalysisRepo) SetTaskID(ctx context.Context, id, taskID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE analyses SET task_id = ? WHERE id = ?`, taskID, id)
	return err
}

func (r *AnalysisRepo) Get(ctx context.Context, id int64) (*domain.AnalysisRecord, error) {
	var (
		a           domain.AnalysisRecord
		ids, status string
		isTask      int
		created     int64
	)
	err := r.db.QueryRowContext(ctx, `SELECT id, connection_id, chat_id, message_ids, input_text, provider, model,
		raw_response, is_task, confidence, message_type, status, error, task_id, latency_ms, created_at
		FROM analyses WHERE id = ?`, id).Scan(&a.ID, &a.ConnectionID, &a.ChatID, &ids, &a.InputText, &a.Provider,
		&a.Model, &a.RawResponse, &isTask, &a.Confidence, &a.MessageType, &status, &a.Error, &a.TaskID, &a.LatencyMs, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get analysis: %w", err)
	}
	_ = json.Unmarshal([]byte(ids), &a.MessageRowIDs)
	a.IsTask = isTask == 1
	a.Status = domain.AnalysisStatus(status)
	a.CreatedAt = fromUnix(created)
	return &a, nil
}

func (r *AnalysisRepo) Stats(ctx context.Context, since time.Time) (*domain.AnalysisStats, error) {
	st := &domain.AnalysisStats{ByProvider: map[string]int{}}
	err := r.db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'ok' AND task_id = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN task_id > 0 THEN 1 ELSE 0 END), 0),
		COALESCE(AVG(CASE WHEN status = 'ok' AND provider <> 'heuristic' THEN latency_ms END), 0)
		FROM analyses WHERE created_at >= ?`, toUnix(since)).
		Scan(&st.Total, &st.Errors, &st.Noise, &st.WithTask, &st.AvgLatencyMs)
	if err != nil {
		return nil, fmt.Errorf("analysis stats: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT provider, COUNT(*) FROM analyses WHERE created_at >= ? GROUP BY provider`, toUnix(since))
	if err != nil {
		return nil, fmt.Errorf("analysis stats by provider: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			p string
			n int
		)
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		st.ByProvider[p] = n
	}
	return st, rows.Err()
}
