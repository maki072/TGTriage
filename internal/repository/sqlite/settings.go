package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tgtriage/internal/domain"
)

// SettingsRepo implements domain.SettingsRepository.
type SettingsRepo struct{ db *sql.DB }

var _ domain.SettingsRepository = (*SettingsRepo)(nil)

func (r *SettingsRepo) All(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (r *SettingsRepo) Get(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	return r.SetMany(ctx, map[string]string{key: value})
}

func (r *SettingsRepo) SetMany(ctx context.Context, values map[string]string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for k, v := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, v, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("save setting %s: %w", k, err)
		}
	}
	return tx.Commit()
}

func (r *SettingsRepo) DeleteExceptPrefix(ctx context.Context, keepPrefix string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM settings WHERE substr(key, 1, ?) <> ?`, len(keepPrefix), keepPrefix)
	return err
}
