package domain

import "time"

// Message is a stored message from a business chat (incoming or outgoing).
type Message struct {
	ID             int64 // local row id
	ConnectionID   string
	ChatID         int64
	MessageID      int // Telegram message id
	SenderID       int64
	SenderName     string
	SenderUsername string
	Outgoing       bool // sent by the owner (or by the bot on owner's behalf)
	Text           string
	SentAt         time.Time
	Analyzed       bool
	AnalysisID     int64
	Deleted        bool
	ViaBot         bool // outgoing text sent by the bot on the owner's behalf, not typed by the owner
}

// ReplyQuery selects the owner's own replies (typed by the owner, personal chats only), newest first.
type ReplyQuery struct {
	ConnectionID string // empty = any personal connection
	ChatID       int64  // 0 = any chat
	ExceptChatID int64  // 0 = no exclusion
	BeforeID     int64  // only rows with id below this; 0 = no limit
	Limit        int
}

// ReplyChat is a chat with the count of the owner's own replies in it.
type ReplyChat struct {
	ConnectionID string
	ChatID       int64
	Count        int
}

// BusinessConnection mirrors Telegram Business connection state.
type BusinessConnection struct {
	ID              string
	UserID          int64
	UserChatID      int64
	UserName        string
	CanReply        bool
	CanReadMessages bool
	Enabled         bool
	UpdatedAt       time.Time
}
