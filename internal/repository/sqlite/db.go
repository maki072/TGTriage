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
	Bots        *BotRepo
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
		Bots:        &BotRepo{db: db},
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
	// v3: task editing, custom reminders, periodic personal reminders, merging
	{
		`ALTER TABLE tasks ADD COLUMN importance TEXT NOT NULL DEFAULT 'medium'`,
		`ALTER TABLE tasks ADD COLUMN remind_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN last_reminded_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN merged_into INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX idx_tasks_remind_at ON tasks (remind_at)`,
		`CREATE INDEX idx_tasks_personal_nudge ON tasks (status, connection_id, last_reminded_at)`,
	},
	// v4: multi-bot — additional bots' tasks/messages/analyses keep using the shared tables,
	// disambiguated by connection_id "helpdesk:<bot_id>"; only hd_users/hd_messages need a real
	// bot_id column, since the same Telegram user id can write to several bots.
	{
		`CREATE TABLE bots (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			token                TEXT NOT NULL UNIQUE,
			username             TEXT NOT NULL DEFAULT '',
			label                TEXT NOT NULL DEFAULT '',
			active               INTEGER NOT NULL DEFAULT 1,
			sensitivity          TEXT NOT NULL DEFAULT '',
			ai_chain             TEXT NOT NULL DEFAULT '[]',
			hd_enabled           INTEGER NOT NULL DEFAULT 1,
			hd_group_id          INTEGER NOT NULL DEFAULT 0,
			hd_triage_enabled    INTEGER NOT NULL DEFAULT 1,
			hd_about             TEXT NOT NULL DEFAULT '',
			hd_greeting_enabled  INTEGER NOT NULL DEFAULT 0,
			hd_greeting_text     TEXT NOT NULL DEFAULT '',
			hd_autoreply_enabled INTEGER NOT NULL DEFAULT 0,
			hd_autoreply_text    TEXT NOT NULL DEFAULT '',
			hd_hours_enabled     INTEGER NOT NULL DEFAULT 0,
			hd_hours_start       TEXT NOT NULL DEFAULT '',
			hd_hours_end         TEXT NOT NULL DEFAULT '',
			hd_hours_days        TEXT NOT NULL DEFAULT '',
			hd_offhours_text     TEXT NOT NULL DEFAULT '',
			hd_reminder_minutes  INTEGER NOT NULL DEFAULT 0,
			created_at           INTEGER NOT NULL,
			updated_at           INTEGER NOT NULL
		)`,
		`CREATE TABLE hd_users_new (
			bot_id          INTEGER NOT NULL DEFAULT 0,
			user_id         INTEGER NOT NULL,
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
			updated_at      INTEGER NOT NULL,
			PRIMARY KEY (bot_id, user_id)
		)`,
		`INSERT INTO hd_users_new (bot_id, user_id, name, username, language_code, source, group_id, topic_id,
			topic_closed, blocked, awaiting_since, reminded_at, last_message_at, created_at, updated_at)
			SELECT 0, user_id, name, username, language_code, source, group_id, topic_id,
				topic_closed, blocked, awaiting_since, reminded_at, last_message_at, created_at, updated_at
			FROM hd_users`,
		`DROP TABLE hd_users`,
		`ALTER TABLE hd_users_new RENAME TO hd_users`,
		`CREATE INDEX idx_hd_users_topic ON hd_users (bot_id, group_id, topic_id)`,
		`CREATE INDEX idx_hd_users_awaiting ON hd_users (bot_id, awaiting_since)`,
		`ALTER TABLE hd_messages ADD COLUMN bot_id INTEGER NOT NULL DEFAULT 0`,
		`DROP INDEX idx_hd_messages_user`,
		`DROP INDEX idx_hd_messages_group`,
		`CREATE INDEX idx_hd_messages_user ON hd_messages (bot_id, user_id, user_msg_id)`,
		`CREATE INDEX idx_hd_messages_group ON hd_messages (bot_id, group_id, group_msg_id)`,
	},
	// v5: spam bans — a banned user's messages are dropped by the bot
	{
		`ALTER TABLE hd_users ADD COLUMN banned INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE hd_users ADD COLUMN banned_at INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX idx_hd_users_banned ON hd_users (bot_id, banned)`,
	},
	// v6: anti-spam — new users may be held (captcha / review), held messages wait in hd_held.
	// Everyone who already wrote to a bot counts as verified.
	{
		`ALTER TABLE hd_users ADD COLUMN verified INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE hd_users ADD COLUMN hold TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hd_users ADD COLUMN spam_flagged INTEGER NOT NULL DEFAULT 0`,
		`UPDATE hd_users SET verified = 1`,
		`CREATE TABLE hd_held (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			bot_id         INTEGER NOT NULL DEFAULT 0,
			user_id        INTEGER NOT NULL,
			message_id     INTEGER NOT NULL,
			media_group_id TEXT NOT NULL DEFAULT '',
			reply_to_id    INTEGER NOT NULL DEFAULT 0,
			text           TEXT NOT NULL DEFAULT '',
			sent_at        INTEGER NOT NULL,
			created_at     INTEGER NOT NULL
		)`,
		`CREATE INDEX idx_hd_held_user ON hd_held (bot_id, user_id)`,
		`ALTER TABLE bots ADD COLUMN hd_spam_screen INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE bots ADD COLUMN hd_spam_captcha INTEGER NOT NULL DEFAULT 0`,
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
