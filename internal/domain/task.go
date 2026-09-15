package domain

import (
	"strings"
	"time"
)

// TaskStatus is a lifecycle state of a task.
type TaskStatus string

const (
	StatusNew           TaskStatus = "new"
	StatusInProgress    TaskStatus = "in_progress"
	StatusSnoozed       TaskStatus = "snoozed"
	StatusDone          TaskStatus = "done"
	StatusFalsePositive TaskStatus = "false_positive"
)

// IsOpen reports whether the task still requires attention.
func (s TaskStatus) IsOpen() bool {
	switch s {
	case StatusNew, StatusInProgress, StatusSnoozed:
		return true
	}
	return false
}

func (s TaskStatus) Valid() bool {
	switch s {
	case StatusNew, StatusInProgress, StatusSnoozed, StatusDone, StatusFalsePositive:
		return true
	}
	return false
}

// Priority of a task.
type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

// ParsePriority normalizes an arbitrary string; unknown values become medium.
func ParsePriority(s string) Priority {
	switch Priority(strings.ToLower(strings.TrimSpace(s))) {
	case PriorityLow:
		return PriorityLow
	case PriorityHigh:
		return PriorityHigh
	case PriorityCritical:
		return PriorityCritical
	default:
		return PriorityMedium
	}
}

// Rank returns sort weight: lower is more urgent.
func (p Priority) Rank() int {
	switch p {
	case PriorityCritical:
		return 0
	case PriorityHigh:
		return 1
	case PriorityMedium:
		return 2
	default:
		return 3
	}
}

// Category of a task.
type Category string

const (
	CategoryBug       Category = "bug"
	CategoryHelp      Category = "help_request"
	CategoryTask      Category = "task"
	CategoryQuestion  Category = "question"
	CategoryDeadline  Category = "deadline"
	CategoryAgreement Category = "agreement"
	CategoryOther     Category = "other"
)

// ParseCategory normalizes an arbitrary string; unknown values become other.
func ParseCategory(s string) Category {
	c := Category(strings.ToLower(strings.TrimSpace(s)))
	switch c {
	case CategoryBug, CategoryHelp, CategoryTask, CategoryQuestion, CategoryDeadline, CategoryAgreement:
		return c
	default:
		return CategoryOther
	}
}

// Task is an actionable item extracted from a conversation.
type Task struct {
	ID               int64
	ConnectionID     string
	ChatID           int64
	SenderID         int64
	SenderName       string
	SenderUsername   string
	SourceMessageIDs []int // Telegram message ids in the source chat
	SourceText       string
	Title            string
	Description      string
	Priority         Priority
	Category         Category
	Deadline         *time.Time
	DraftReply       string
	ReplyStrategy    string
	Confidence       float64
	Status           TaskStatus
	PrevStatus       TaskStatus // status to restore after snooze
	SnoozeUntil      *time.Time
	AnalysisID       int64
	Provider         string
	Model            string
	ReplySentAt      *time.Time
	ReplyText        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ClosedAt         *time.Time
}

// FirstSourceMessageID returns the first Telegram message id of the source batch (0 if none).
func (t *Task) FirstSourceMessageID() int {
	if len(t.SourceMessageIDs) == 0 {
		return 0
	}
	return t.SourceMessageIDs[0]
}

// LastSourceMessageID returns the last Telegram message id of the source batch (0 if none).
func (t *Task) LastSourceMessageID() int {
	if len(t.SourceMessageIDs) == 0 {
		return 0
	}
	return t.SourceMessageIDs[len(t.SourceMessageIDs)-1]
}

// HasChat reports whether the task is linked to a Business chat, i.e. the owner can reply from the bot.
// Tasks created from messages forwarded to the bot have no chat.
func (t *Task) HasChat() bool { return t.ChatID != 0 }

// IsOverdue reports whether an open task has passed its deadline.
func (t *Task) IsOverdue(now time.Time) bool {
	return t.Deadline != nil && t.Status.IsOpen() && t.Deadline.Before(now)
}

// IsHelpdesk reports whether the task is a support desk ticket.
func (t *Task) IsHelpdesk() bool { return t.ConnectionID == HelpdeskConnectionID }

// TaskScope narrows tasks to support desk tickets or the owner's personal tasks.
type TaskScope string

const (
	ScopeAll      TaskScope = ""
	ScopeHelpdesk TaskScope = "helpdesk"
	ScopePersonal TaskScope = "personal"
)

// ParseTaskScope normalizes a scope name; unknown values mean all tasks.
func ParseTaskScope(s string) TaskScope {
	switch TaskScope(s) {
	case ScopeHelpdesk, ScopePersonal:
		return TaskScope(s)
	}
	return ScopeAll
}

// TaskFilter selects tasks for listing.
type TaskFilter struct {
	Statuses   []TaskStatus // empty = any
	Priorities []Priority   // empty = any
	Scope      TaskScope
	ChatID     int64 // 0 = any
	Limit      int
	Offset     int
}
