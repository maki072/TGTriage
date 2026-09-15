// Package sqlite implements domain repositories on top of embedded SQLite (pure Go, WAL mode).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary static (CGO_ENABLED=0)
)

// Store bundles all repositories sharing one database handle.
type Store struct {
	db          *sql.DB
	Messages    *MessageRepo
	Tasks       *TaskRepo
	Analyses    *AnalysisRepo
	Settings    *SettingsRepo
	Connections *ConnectionRepo
	Helpdesk    *HelpdeskRepo
}

// Open opens (and creates if needed) the database, applies pragmas and migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := path +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer connection: the workload is tiny and this rules out SQLITE_BUSY entirely.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{
		db:          db,
		Messages:    &MessageRepo{db: db},
		Tasks:       &TaskRepo{db: db},
		Analyses:    &AnalysisRepo{db: db},
		Settings:    &SettingsRepo{db: db},
		Connections: &ConnectionRepo{db: db},
		Helpdesk:    &HelpdeskRepo{db: db},
	}, nil
}

// Close checkpoints WAL and closes the database.
func (s *Store) Close() error {
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

var migrations = [][]string{
	// v1: initial schema
	{
		`CREATE TABLE business_connections (
			id                TEXT PRIMARY KEY,
			user_id           INTEGER NOT NULL,
			user_chat_id      INTEGER NOT NULL,
			user_name         TEXT NOT NULL DEFAULT '',
			can_reply         INTEGER NOT NULL DEFAULT 0,
			can_read_messages INTEGER NOT NULL DEFAULT 0,
			is_enabled        INTEGER NOT NULL DEFAULT 1,
			updated_at        INTEGER NOT NULL
		)`,
		`CREATE TABLE messages (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			connection_id   TEXT NOT NULL,
			chat_id         INTEGER NOT NULL,
			message_id      INTEGER NOT NULL,
			sender_id       INTEGER NOT NULL,
			sender_name     TEXT NOT NULL DEFAULT '',
			sender_username TEXT NOT NULL DEFAULT '',
			outgoing        INTEGER NOT NULL DEFAULT 0,
			text            TEXT NOT NULL,
			sent_at         INTEGER NOT NULL,
			analyzed        INTEGER NOT NULL DEFAULT 0,
			analysis_id     INTEGER NOT NULL DEFAULT 0,
			deleted         INTEGER NOT NULL DEFAULT 0,
			created_at      INTEGER NOT NULL,
			UNIQUE (connection_id, chat_id, message_id)
		)`,
		`CREATE INDEX idx_messages_chat ON messages (connection_id, chat_id, id)`,
		`CREATE INDEX idx_messages_pending ON messages (analyzed, outgoing, deleted)`,
		`CREATE INDEX idx_messages_sent_at ON messages (sent_at)`,
		`CREATE TABLE analyses (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			connection_id TEXT NOT NULL,
			chat_id       INTEGER NOT NULL,
			message_ids   TEXT NOT NULL DEFAULT '[]',
			input_text    TEXT NOT NULL DEFAULT '',
			provider      TEXT NOT NULL DEFAULT '',
			model         TEXT NOT NULL DEFAULT '',
			raw_response  TEXT NOT NULL DEFAULT '',
			is_task       INTEGER NOT NULL DEFAULT 0,
			confidence    REAL NOT NULL DEFAULT 0,
			message_type  TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL,
			error         TEXT NOT NULL DEFAULT '',
			task_id       INTEGER NOT NULL DEFAULT 0,
			latency_ms    INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_analyses_created ON analyses (created_at)`,
		`CREATE TABLE tasks (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			connection_id      TEXT NOT NULL,
			chat_id            INTEGER NOT NULL,
			sender_id          INTEGER NOT NULL,
			sender_name        TEXT NOT NULL DEFAULT '',
			sender_username    TEXT NOT NULL DEFAULT '',
			source_message_ids TEXT NOT NULL DEFAULT '[]',
			source_text        TEXT NOT NULL DEFAULT '',
			title              TEXT NOT NULL,
			description        TEXT NOT NULL DEFAULT '',
			priority           TEXT NOT NULL,
			priority_rank      INTEGER NOT NULL,
			category           TEXT NOT NULL,
			deadline           INTEGER NOT NULL DEFAULT 0,
			draft_reply        TEXT NOT NULL DEFAULT '',
			reply_strategy     TEXT NOT NULL DEFAULT '',
			confidence         REAL NOT NULL DEFAULT 0,
			status             TEXT NOT NULL,
			prev_status        TEXT NOT NULL DEFAULT '',
			snooze_until       INTEGER NOT NULL DEFAULT 0,
			analysis_id        INTEGER NOT NULL DEFAULT 0,
			provider           TEXT NOT NULL DEFAULT '',
			model              TEXT NOT NULL DEFAULT '',
			reply_sent_at      INTEGER NOT NULL DEFAULT 0,
			reply_text         TEXT NOT NULL DEFAULT '',
			created_at         INTEGER NOT NULL,
			updated_at         INTEGER NOT NULL,
			closed_at          INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX idx_tasks_status ON tasks (status, priority_rank, deadline)`,
		`CREATE INDEX idx_tasks_chat ON tasks (chat_id, status)`,
		`CREATE INDEX idx_tasks_snooze ON tasks (status, snooze_until)`,
		`CREATE TABLE settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
	// v2: support desk
	{
		`CREATE TABLE hd_users (
			user_id         INTEGER PRIMARY KEY,
			name            TEXT NOT NULL DEFAULT '',
			username        TEXT NOT NULL DEFAULT '',
			language_code   TEXT NOT NULL DEFAULT '',
			source          TEXT NOT NULL DEFAULT '',
			group_id        INTEGER NOT NULL DEFAULT 0,
			topic_id        INTEGER NOT NULL DEFAULT 0,
			topic_closed    INTEGER NOT NULL DEFAULT 0,
			blocked         INTEGER NOT NULL DEFAULT 0,
			awaiting_since  INTEGER NOT NULL DEFAULT 0,
			reminded_at     INTEGER NOT NULL DEFAULT 0,
			last_message_at INTEGER NOT NULL DEFAULT 0,
			created_at      INTEGER NOT NULL,
			updated_at      INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_hd_users_topic ON hd_users (group_id, topic_id)`,
		`CREATE INDEX idx_hd_users_awaiting ON hd_users (awaiting_since)`,
		`CREATE TABLE hd_messages (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id      INTEGER NOT NULL,
			direction    TEXT NOT NULL,
			user_msg_id  INTEGER NOT NULL DEFAULT 0,
			group_id     INTEGER NOT NULL DEFAULT 0,
			group_msg_id INTEGER NOT NULL DEFAULT 0,
			operator_id  INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_hd_messages_user ON hd_messages (user_id, user_msg_id)`,
		`CREATE INDEX idx_hd_messages_group ON hd_messages (group_id, group_msg_id)`,
		`CREATE INDEX idx_hd_messages_created ON hd_messages (group_id, created_at)`,
		`CREATE TABLE hd_cards (
			task_id    INTEGER NOT NULL,
			chat_id    INTEGER NOT NULL,
			topic_id   INTEGER NOT NULL DEFAULT 0,
			message_id INTEGER NOT NULL,
			PRIMARY KEY (task_id, chat_id, message_id)
		)`,
		`CREATE INDEX idx_tasks_connection ON tasks (connection_id, status)`,
	},
}

// Backup writes a consistent, compacted copy of the database to path (which must not exist).
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := current; i < len(migrations); i++ {
		version := i + 1
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		for _, stmt := range migrations[i] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func ptrToUnix(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func ptrFromUnix(v int64) *time.Time {
	if v == 0 {
		return nil
	}
	t := time.Unix(v, 0)
	return &t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
