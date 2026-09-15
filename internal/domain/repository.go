package domain

import (
	"context"
	"time"
)

// MessageRepository stores business chat history.
type MessageRepository interface {
	// Save inserts a message; returns false if it already exists (idempotent).
	Save(ctx context.Context, m *Message) (bool, error)
	UpdateText(ctx context.Context, connectionID string, chatID int64, messageID int, text string) error
	// Find returns a message by its Telegram id in the chat.
	Find(ctx context.Context, connectionID string, chatID int64, messageID int) (*Message, error)
	MarkDeleted(ctx context.Context, connectionID string, chatID int64, messageIDs []int) error
	GetByIDs(ctx context.Context, ids []int64) ([]Message, error)
	// History returns up to limit messages before row id beforeID, oldest first.
	History(ctx context.Context, connectionID string, chatID int64, beforeID int64, limit int) ([]Message, error)
	// Pending returns incoming messages not yet analyzed.
	Pending(ctx context.Context) ([]Message, error)
	MarkAnalyzed(ctx context.Context, ids []int64, analysisID int64) error
	DeleteOlderThan(ctx context.Context, before time.Time) (int64, error)
}

// TaskRepository stores tasks.
type TaskRepository interface {
	Create(ctx context.Context, t *Task) error
	Update(ctx context.Context, t *Task) error
	Get(ctx context.Context, id int64) (*Task, error)
	List(ctx context.Context, f TaskFilter) ([]Task, int, error)
	DueSnoozed(ctx context.Context, now time.Time) ([]Task, error)
	CountByStatus(ctx context.Context, scope TaskScope, since time.Time) (map[TaskStatus]int, error)
	CountOverdue(ctx context.Context, scope TaskScope, now time.Time) (int, error)
}

// AnalysisRepository stores LLM call audit log.
type AnalysisRepository interface {
	Create(ctx context.Context, r *AnalysisRecord) error
	SetTaskID(ctx context.Context, id, taskID int64) error
	Get(ctx context.Context, id int64) (*AnalysisRecord, error)
	Stats(ctx context.Context, since time.Time) (*AnalysisStats, error)
}

// SettingsRepository is a simple key-value store.
type SettingsRepository interface {
	All(ctx context.Context) (map[string]string, error)
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
	SetMany(ctx context.Context, values map[string]string) error
	DeleteExceptPrefix(ctx context.Context, keepPrefix string) error
}

// ConnectionRepository stores business connections.
type ConnectionRepository interface {
	Upsert(ctx context.Context, c *BusinessConnection) error
	Get(ctx context.Context, id string) (*BusinessConnection, error)
	LatestForUser(ctx context.Context, userID int64) (*BusinessConnection, error)
}
