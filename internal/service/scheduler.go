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
	// TaskReminder fires a one-off custom reminder set on a task (see TaskService.SetReminder).
	TaskReminder(ctx context.Context, t *domain.Task)
	// PersonalNudge fires a repeated reminder about an open personal-chat task.
	PersonalNudge(ctx context.Context, t *domain.Task)
}

// Scheduler runs periodic jobs: snooze reminders, morning digest, history retention, helpdesk
// reminders about unanswered users and database backups.
type Scheduler struct {
	tasks     *TaskService
	settings  *SettingsService
	messages  domain.MessageRepository
	notifiers *BotRegistry[SchedulerNotifier]
	helpdesks *BotRegistry[*HelpdeskService] // one entry per bot with the desk on; optional
	backups   *BackupService                 // optional
	interval  time.Duration
	log       *slog.Logger
}

func NewScheduler(tasks *TaskService, settings *SettingsService, messages domain.MessageRepository,
	notifier SchedulerNotifier, helpdesk *HelpdeskService, backups *BackupService, log *slog.Logger) *Scheduler {
	helpdesks := NewBotRegistry[*HelpdeskService](nil)
	if helpdesk != nil {
		helpdesks.Set(0, helpdesk)
	}
	return &Scheduler{
		tasks: tasks, settings: settings, messages: messages, notifiers: NewBotRegistry(notifier), helpdesks: helpdesks,
		backups: backups, interval: 30 * time.Second, log: log.With("component", "scheduler"),
	}
}

// RegisterBot and UnregisterBot let the bot runtime manager plug an additional bot's
// delivery.Bot/HelpdeskService in and out as it starts and stops, without a restart.
func (s *Scheduler) RegisterBot(botID int64, n SchedulerNotifier, h *HelpdeskService) {
	s.notifiers.Set(botID, n)
	s.helpdesks.Set(botID, h)
}

func (s *Scheduler) UnregisterBot(botID int64) {
	s.notifiers.Unset(botID)
	s.helpdesks.Unset(botID)
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
	s.fireReminders(ctx)
	s.personalNudges(ctx)
	s.digest(ctx)
	s.cleanup(ctx)
	for _, h := range s.helpdesks.All() {
		h.CheckReminders(ctx)
	}
	if s.backups != nil {
		s.backups.RunIfDue(ctx)
	}
}

// notifyTask calls fn on the SchedulerNotifier of the bot t belongs to; a task whose bot runtime
// isn't currently live is silently skipped (it's still woken/reminded in the DB either way).
func (s *Scheduler) notifyTask(t *domain.Task, fn func(SchedulerNotifier, *domain.Task)) {
	if n, ok := s.notifiers.For(domain.ParseHelpdeskBotID(t.ConnectionID)); ok {
		fn(n, t)
	}
}

func (s *Scheduler) wakeSnoozed(ctx context.Context) {
	woken, err := s.tasks.WakeSnoozed(ctx)
	if err != nil {
		s.log.Error("wake snoozed", "err", err)
		return
	}
	for i := range woken {
		s.notifyTask(&woken[i], func(n SchedulerNotifier, t *domain.Task) { n.SnoozeFired(ctx, t) })
	}
}

func (s *Scheduler) fireReminders(ctx context.Context) {
	fired, err := s.tasks.WakeReminders(ctx)
	if err != nil {
		s.log.Error("fire reminders", "err", err)
		return
	}
	for i := range fired {
		s.notifyTask(&fired[i], func(n SchedulerNotifier, t *domain.Task) { n.TaskReminder(ctx, t) })
	}
}

func (s *Scheduler) personalNudges(ctx context.Context) {
	minutes := s.settings.Get().PersonalReminderMinutes
	nudged, err := s.tasks.NudgePersonal(ctx, minutes)
	if err != nil {
		s.log.Error("personal nudges", "err", err)
		return
	}
	if n, ok := s.notifiers.For(0); ok {
		for i := range nudged {
			n.PersonalNudge(ctx, &nudged[i])
		}
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
	d, err := s.tasks.BuildDigest(ctx, domain.ScopeAll, "")
	if err != nil {
		s.log.Error("build digest", "err", err)
		return
	}
	if n, ok := s.notifiers.For(0); ok {
		n.Digest(ctx, d)
	}
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
	// DeleteMessagesOlderThan is bot-agnostic (a single hd_messages table), so any one live desk's
	// CleanupMessages call covers every bot; only call it once.
	if hs := s.helpdesks.All(); len(hs) > 0 {
		if _, err := hs[0].CleanupMessages(ctx, before); err != nil {
			s.log.Error("cleanup helpdesk mappings", "err", err)
		}
	}
	_ = s.settings.SetMeta(ctx, "last_cleanup", today)
	if n > 0 {
		s.log.Info("old messages removed", "count", n)
	}
}
