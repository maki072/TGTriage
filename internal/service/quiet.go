package service

import (
	"context"
	"time"

	"tgtriage/internal/domain"
)

type silentKey struct{}

// WithSilent marks a notification as one that must not make the owner's phone ring: the delivery layer
// sends it with Telegram's disable_notification.
func WithSilent(ctx context.Context) context.Context {
	return context.WithValue(ctx, silentKey{}, true)
}

// IsSilent reports whether ctx carries WithSilent.
func IsSilent(ctx context.Context) bool { v, _ := ctx.Value(silentKey{}).(bool); return v }

// ownerActive reports whether the owner wrote in the chat within the last minutes — they are in the
// middle of the conversation and see it in Telegram themselves, the bot's card need not ring.
func ownerActive(history []domain.Message, now time.Time, minutes int) bool {
	if minutes <= 0 {
		return false
	}
	window := time.Duration(minutes) * time.Minute
	for _, m := range history {
		if m.Outgoing && now.Sub(m.SentAt) <= window {
			return true
		}
	}
	return false
}

// threadTask picks the open task a new message of a personal chat belongs to by recency alone: a chat
// keeps one task per conversation, so a follow-up that the model did not link to a task joins the
// newest one that was touched within window. Snoozed tasks are left alone — the owner put them aside.
func threadTask(open []domain.Task, now time.Time, window time.Duration) *domain.Task {
	if window <= 0 {
		return nil
	}
	var best *domain.Task
	for i := range open {
		t := &open[i]
		if t.Status != domain.StatusNew && t.Status != domain.StatusInProgress {
			continue
		}
		touched := t.UpdatedAt
		if t.CreatedAt.After(touched) {
			touched = t.CreatedAt
		}
		if now.Sub(touched) > window {
			continue
		}
		if best == nil || t.ID > best.ID {
			best = t
		}
	}
	return best
}
