package domain

import (
	"context"
	"time"
)

// HelpdeskConnectionID marks messages, analyses and tasks of the support desk: they share the
// storage and triage pipeline with Telegram Business chats, keyed by the user's private chat id.
const HelpdeskConnectionID = "helpdesk"

// HelpdeskUser is someone who writes to the bot for support. Operators talk to them in a forum
// topic of the helpdesk group; the user only ever sees the bot.
type HelpdeskUser struct {
	UserID        int64 // equals the private chat id with the bot
	Name          string
	Username      string
	LanguageCode  string
	Source        string // /start payload of the first visit
	GroupID       int64  // group the topic belongs to (the configured group may change)
	TopicID       int    // message_thread_id; 0 = no topic yet
	TopicClosed   bool
	Blocked       bool       // the user blocked the bot
	AwaitingSince *time.Time // first user message not answered by an operator yet
	RemindedAt    *time.Time // last reminder about AwaitingSince
	LastMessageAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// HasTopic reports whether the user has a topic in the given group.
func (u *HelpdeskUser) HasTopic(groupID int64) bool { return u.TopicID != 0 && u.GroupID == groupID }

// HelpdeskDirection is the way a relayed message went.
type HelpdeskDirection string

const (
	HelpdeskIn  HelpdeskDirection = "in"  // user → topic
	HelpdeskOut HelpdeskDirection = "out" // operator → user
)

// HelpdeskMessage maps a message in the private chat to its copy in the topic.
type HelpdeskMessage struct {
	ID         int64
	UserID     int64
	Direction  HelpdeskDirection
	UserMsgID  int
	GroupID    int64
	GroupMsgID int
	OperatorID int64
	CreatedAt  time.Time
}

// HelpdeskCard is a ticket card message posted into the group.
type HelpdeskCard struct {
	TaskID    int64
	ChatID    int64
	TopicID   int
	MessageID int
}

// HelpdeskUserFilter selects users for the dialogs list.
type HelpdeskUserFilter struct {
	AwaitingOnly bool
	Query        string
	Limit        int
	Offset       int
}

// HelpdeskRepository stores support desk users, message mappings and ticket cards.
type HelpdeskRepository interface {
	GetUser(ctx context.Context, userID int64) (*HelpdeskUser, error)
	UserByTopic(ctx context.Context, groupID int64, topicID int) (*HelpdeskUser, error)
	SaveUser(ctx context.Context, u *HelpdeskUser) error
	ListUsers(ctx context.Context, f HelpdeskUserFilter) ([]HelpdeskUser, int, error)
	// DueReminders returns users waiting since before cutoff and not reminded after cutoff.
	DueReminders(ctx context.Context, cutoff time.Time) ([]HelpdeskUser, error)

	SaveMessage(ctx context.Context, m *HelpdeskMessage) error
	MessageByUserMsg(ctx context.Context, userID int64, userMsgID int) (*HelpdeskMessage, error)
	MessageByGroupMsg(ctx context.Context, groupID int64, groupMsgID int) (*HelpdeskMessage, error)
	// MessagesAround returns incoming mappings created within ±window of t in the group.
	MessagesAround(ctx context.Context, groupID int64, t time.Time, window time.Duration) ([]HelpdeskMessage, error)
	DeleteMessagesOlderThan(ctx context.Context, before time.Time) (int64, error)

	SaveCard(ctx context.Context, c HelpdeskCard) error
	Cards(ctx context.Context, taskID int64) ([]HelpdeskCard, error)
}
