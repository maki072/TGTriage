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
