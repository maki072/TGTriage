// Package domain contains core entities, value objects and repository ports.
// It has no dependencies on infrastructure (Telegram, SQL, LLM providers).
package domain

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrNoConnection  = errors.New("business connection is not available")
	ErrCannotReply   = errors.New("bot has no permission to reply in this chat")
	ErrEmptyReply    = errors.New("reply text is empty")
	ErrInvalidInput  = errors.New("invalid input")
	ErrProviderUnset = errors.New("AI provider is not configured")
)
