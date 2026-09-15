// Package domain contains core entities, value objects and repository ports.
// It has no dependencies on infrastructure (Telegram, SQL, LLM providers).
package domain

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrNoConnection  = errors.New("business connection is not available")
	ErrCannotReply   = errors.New("bot has no permission to reply in this chat")
	ErrNoSourceChat  = errors.New("task is not linked to a chat")
	ErrEmptyReply    = errors.New("reply text is empty")
	ErrInvalidInput  = errors.New("invalid input")
	ErrProviderUnset = errors.New("AI provider is not configured")
	ErrHelpdeskOff   = errors.New("helpdesk is not enabled")
	ErrUserBlocked   = errors.New("the user blocked the bot")
	ErrForbidden     = errors.New("forbidden")
	ErrTopicGone     = errors.New("forum topic was deleted")
	ErrTopicClosed   = errors.New("forum topic is closed")
)
