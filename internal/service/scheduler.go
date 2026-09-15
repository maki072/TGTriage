package service

import (
	"context"
	"log/slog"
	"time"

	"tgtriage/internal/domain"
)

// SchedulerNotifier delivers scheduled events to the owner.
type SchedulerNotifier interface {
	SnoozeFired(ctx context.Context, t *domain.Task)
	Digest(ctx context.Context, d *Digest)
}

// Scheduler runs periodic jobs: snooze reminders, morning digest, history retention, helpdesk
// reminders about unanswered users and database backups.
type Scheduler struct {
	tasks    *TaskService
	settings *SettingsService
	messages domain.MessageRepository
	notifier SchedulerNotifier
	helpdesk *HelpdeskService // optional
	backups  *BackupService   // optional
	interval time.Duration
	log      *slog.Logger
}

func NewScheduler(tasks *TaskService, settings *SettingsService, messages domain.MessageRepository,
	notifier SchedulerNotifier, helpdesk *HelpdeskService, backups *BackupService, log *slog.Logger) *Scheduler {
	return &Scheduler{
		tasks: tasks, settings: settings, messages: messages, notifier: notifier, helpdesk: helpdesk, backups: backups,
		interval: 30 * time.Second, log: log.With("component", "scheduler"),
	}
}

// Run blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic in scheduler", "panic", r)
		}
	}()
	s.wakeSnoozed(ctx)
	s.digest(ctx)
	s.cleanup(ctx)
	if s.helpdesk != nil {
		s.helpdesk.CheckReminders(ctx)
	}
	if s.backups != nil {
		s.backups.RunIfDue(ctx)
	}
}

func (s *Scheduler) wakeSnoozed(ctx context.Context) {
	woken, err := s.tasks.WakeSnoozed(ctx)
	if err != nil {
		s.log.Error("wake snoozed", "err", err)
		return
	}
	for i := range woken {
		s.notifier.SnoozeFired(ctx, &woken[i])
	}
}

// digest sends the morning digest once a day within 3 hours after the configured time.
func (s *Scheduler) digest(ctx context.Context) {
	st := s.settings.Get()
	if !st.DigestEnabled {
		return
	}
	h, m, err := ParseClock(st.DigestTime)
	if err != nil {
		return
	}
	loc := s.settings.Location()
	now := time.Now().In(loc)
	target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
	if now.Before(target) || now.Sub(target) > 3*time.Hour {
		return
	}
	today := now.Format(time.DateOnly)
	last, err := s.settings.Meta(ctx, "last_digest")
	if err != nil || last == today {
		return
	}
	// mark first: a failed delivery must not spam the owner every tick
	if err := s.settings.SetMeta(ctx, "last_digest", today); err != nil {
		s.log.Error("save digest mark", "err", err)
		return
	}
	d, err := s.tasks.BuildDigest(ctx, domain.ScopeAll)
	if err != nil {
		s.log.Error("build digest", "err", err)
		return
	}
	s.notifier.Digest(ctx, d)
}

func (s *Scheduler) cleanup(ctx context.Context) {
	days := s.settings.Get().RetentionDays
	if days <= 0 {
		return
	}
	today := time.Now().In(s.settings.Location()).Format(time.DateOnly)
	if last, err := s.settings.Meta(ctx, "last_cleanup"); err != nil || last == today {
		return
	}
	before := time.Now().AddDate(0, 0, -days)
	n, err := s.messages.DeleteOlderThan(ctx, before)
	if err != nil {
		s.log.Error("cleanup messages", "err", err)
		return
	}
	if s.helpdesk != nil {
		if _, err := s.helpdesk.CleanupMessages(ctx, before); err != nil {
			s.log.Error("cleanup helpdesk mappings", "err", err)
		}
	}
	_ = s.settings.SetMeta(ctx, "last_cleanup", today)
	if n > 0 {
		s.log.Info("old messages removed", "count", n)
	}
}
