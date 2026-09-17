package service

import "sync"

// BotRegistry resolves a per-bot implementation of a delivery-side interface (Notifier,
// TaskObserver, HelpdeskReplier): the main bot's value is fixed once, additional bots register and
// unregister as their runtime starts and stops (see BotManager) so a service shared by every bot
// never ends up talking to the wrong bot's Telegram client.
type BotRegistry[T comparable] struct {
	mu    sync.RWMutex
	main  T
	extra map[int64]T
}

func NewBotRegistry[T comparable](main T) *BotRegistry[T] {
	return &BotRegistry[T]{main: main, extra: map[int64]T{}}
}

// Set registers v for botID (0 = the main bot).
func (r *BotRegistry[T]) Set(botID int64, v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if botID == 0 {
		r.main = v
		return
	}
	r.extra[botID] = v
}

// Unset clears whatever is registered for botID.
func (r *BotRegistry[T]) Unset(botID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if botID == 0 {
		var zero T
		r.main = zero
		return
	}
	delete(r.extra, botID)
}

// For returns the value registered for botID (0 = main); ok is false when nothing is registered —
// the main bot hasn't been wired yet, or an additional bot's runtime isn't currently live.
func (r *BotRegistry[T]) For(botID int64) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if botID == 0 {
		var zero T
		return r.main, r.main != zero
	}
	v, ok := r.extra[botID]
	return v, ok
}

// All returns every currently-live value (main first, if set), for jobs that must visit every bot —
// e.g. the scheduler checking reminders in each bot's own helpdesk group.
func (r *BotRegistry[T]) All() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]T, 0, len(r.extra)+1)
	var zero T
	if r.main != zero {
		out = append(out, r.main)
	}
	for _, v := range r.extra {
		out = append(out, v)
	}
	return out
}
