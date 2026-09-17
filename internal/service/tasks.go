package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// ReplySender sends messages into business chats on behalf of the owner.
type ReplySender interface {
	SendBusinessText(ctx context.Context, connectionID string, chatID int64, text string) (int, error)
	MarkRead(ctx context.Context, connectionID string, chatID int64, messageID int) error
}

// HelpdeskReplier sends a reply to a support desk user.
type HelpdeskReplier interface {
	ReplyToUser(ctx context.Context, userID int64, text string) error
}

// TaskObserver is told about every task change made through TaskService (status, snooze, replies).
type TaskObserver interface {
	TaskChanged(ctx context.Context, t *domain.Task)
}

// TaskService implements task management use cases.
type TaskService struct {
	tasks     domain.TaskRepository
	messages  domain.MessageRepository
	analyses  domain.AnalysisRepository
	conns     *ConnectionService
	settings  *SettingsService
	sender    ReplySender
	helpdesks *BotRegistry[HelpdeskReplier]
	observers *BotRegistry[TaskObserver]
	log       *slog.Logger
}

func NewTaskService(tasks domain.TaskRepository, messages domain.MessageRepository, analyses domain.AnalysisRepository,
	conns *ConnectionService, settings *SettingsService, sender ReplySender, log *slog.Logger) *TaskService {
	return &TaskService{tasks: tasks, messages: messages, analyses: analyses, conns: conns, settings: settings,
		sender: sender, helpdesks: NewBotRegistry[HelpdeskReplier](nil), observers: NewBotRegistry[TaskObserver](nil),
		log: log.With("component", "tasks")}
}

// SetHelpdesk wires replies to the main bot's support desk tickets.
func (s *TaskService) SetHelpdesk(h HelpdeskReplier) { s.helpdesks.Set(0, h) }

// SetObserver wires the main bot's task change observer (ticket cards in its helpdesk group).
func (s *TaskService) SetObserver(o TaskObserver) { s.observers.Set(0, o) }

// RegisterBot and UnregisterBot let the bot runtime manager plug an additional bot's
// HelpdeskService/delivery.Bot in and out as it starts and stops, without a restart.
func (s *TaskService) RegisterBot(botID int64, h HelpdeskReplier, o TaskObserver) {
	s.helpdesks.Set(botID, h)
	s.observers.Set(botID, o)
}

func (s *TaskService) UnregisterBot(botID int64) {
	s.helpdesks.Unset(botID)
	s.observers.Unset(botID)
}

func (s *TaskService) changed(ctx context.Context, t *domain.Task) {
	o, ok := s.observers.For(domain.ParseHelpdeskBotID(t.ConnectionID))
	if !ok {
		return
	}
	cp := *t
	actorCtx := WithActor(context.WithoutCancel(ctx), ActorFrom(ctx))
	go func() {
		ctx, cancel := context.WithTimeout(actorCtx, 30*time.Second)
		defer cancel()
		o.TaskChanged(ctx, &cp)
	}()
}

func (s *TaskService) Get(ctx context.Context, id int64) (*domain.Task, error) {
	return s.tasks.Get(ctx, id)
}

func (s *TaskService) List(ctx context.Context, f domain.TaskFilter) ([]domain.Task, int, error) {
	return s.tasks.List(ctx, f)
}

// SetStatus changes task status (in progress, done, false positive, reopen).
func (s *TaskService) SetStatus(ctx context.Context, id int64, status domain.TaskStatus) (*domain.Task, error) {
	if !status.Valid() || status == domain.StatusSnoozed {
		return nil, domain.ErrInvalidInput
	}
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	now := time.Now()
	t.Status = status
	t.PrevStatus = ""
	t.SnoozeUntil = nil
	if status.IsOpen() {
		t.ClosedAt = nil
	} else {
		t.ClosedAt = &now
	}
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.log.Info("task status changed", "task_id", t.ID, "from", prev, "to", status)
	if status == domain.StatusInProgress && prev != domain.StatusInProgress && s.settings.Get().MarkReadOnWork {
		s.markRead(ctx, t)
	}
	s.changed(ctx, t)
	return t, nil
}

func (s *TaskService) markRead(ctx context.Context, t *domain.Task) {
	msgID := t.LastSourceMessageID()
	if msgID == 0 || t.IsHelpdesk() {
		return
	}
	conn, err := s.conns.Resolve(ctx, t.ConnectionID)
	if err != nil || !conn.CanReadMessages {
		return
	}
	if err := s.sender.MarkRead(ctx, conn.ID, t.ChatID, msgID); err != nil {
		s.log.Warn("mark message as read failed", "task_id", t.ID, "err", err)
	}
}

// Snooze hides the task until the given moment, then the scheduler reminds the owner.
func (s *TaskService) Snooze(ctx context.Context, id int64, until time.Time) (*domain.Task, error) {
	if !until.After(time.Now()) {
		return nil, domain.ErrInvalidInput
	}
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	switch t.Status {
	case domain.StatusNew, domain.StatusInProgress:
		t.PrevStatus = t.Status
	case domain.StatusSnoozed:
		if t.PrevStatus == "" {
			t.PrevStatus = domain.StatusNew
		}
	default:
		t.PrevStatus = domain.StatusNew
	}
	t.Status = domain.StatusSnoozed
	t.SnoozeUntil = &until
	t.ClosedAt = nil
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.changed(ctx, t)
	return t, nil
}

// EditInput is the set of task fields the owner can change by hand.
type EditInput struct {
	Title       string
	Description string
	Priority    domain.Priority
	Importance  domain.Priority
}

// Edit updates a task's text and its urgency/importance.
func (s *TaskService) Edit(ctx context.Context, id int64, in EditInput) (*domain.Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, domain.ErrInvalidInput
	}
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	t.Title = title
	t.Description = strings.TrimSpace(in.Description)
	t.Priority = domain.ParsePriority(string(in.Priority))
	t.Importance = domain.ParsePriority(string(in.Importance))
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.log.Info("task edited", "task_id", t.ID)
	s.changed(ctx, t)
	return t, nil
}

// SetReminder schedules a one-off custom reminder about the task, independent of its status/snooze.
func (s *TaskService) SetReminder(ctx context.Context, id int64, at time.Time) (*domain.Task, error) {
	if !at.After(time.Now()) {
		return nil, domain.ErrInvalidInput
	}
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	t.RemindAt = &at
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.changed(ctx, t)
	return t, nil
}

// ClearReminder cancels a task's custom reminder.
func (s *TaskService) ClearReminder(ctx context.Context, id int64) (*domain.Task, error) {
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	t.RemindAt = nil
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.changed(ctx, t)
	return t, nil
}

// WakeReminders fires due custom reminders (see SetReminder) and clears them.
func (s *TaskService) WakeReminders(ctx context.Context) ([]domain.Task, error) {
	due, err := s.tasks.DueRemind(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	fired := make([]domain.Task, 0, len(due))
	for _, t := range due {
		t.RemindAt = nil
		if err := s.tasks.Update(ctx, &t); err != nil {
			s.log.Error("clear fired reminder", "task_id", t.ID, "err", err)
			continue
		}
		s.changed(ctx, &t)
		fired = append(fired, t)
	}
	return fired, nil
}

// NudgePersonal re-notifies about open personal-chat tasks that have been waiting at least
// minutes since creation or the last nudge. minutes<=0 disables the feature.
func (s *TaskService) NudgePersonal(ctx context.Context, minutes int) ([]domain.Task, error) {
	if minutes <= 0 {
		return nil, nil
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
	due, err := s.tasks.DuePersonalNudge(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	nudged := make([]domain.Task, 0, len(due))
	for _, t := range due {
		t.LastRemindedAt = &now
		if err := s.tasks.Update(ctx, &t); err != nil {
			s.log.Error("mark personal nudge", "task_id", t.ID, "err", err)
			continue
		}
		nudged = append(nudged, t)
	}
	return nudged, nil
}

// Merge folds source's data into target and closes source. Both tasks must be distinct and not
// already merged.
func (s *TaskService) Merge(ctx context.Context, sourceID, targetID int64) (*domain.Task, error) {
	if sourceID == targetID {
		return nil, domain.ErrInvalidInput
	}
	src, err := s.tasks.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	target, err := s.tasks.Get(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if src.IsMerged() || target.IsMerged() {
		return nil, domain.ErrInvalidInput
	}
	target.SourceMessageIDs = append(target.SourceMessageIDs, src.SourceMessageIDs...)
	if strings.TrimSpace(src.SourceText) != "" {
		target.SourceText = strings.TrimSpace(target.SourceText + "\n\n---\n" + src.SourceText)
	}
	if d := strings.TrimSpace(src.Description); d != "" && d != strings.TrimSpace(target.Description) {
		target.Description = strings.TrimSpace(target.Description + "\n\n" + d)
	}
	if src.Priority.Rank() < target.Priority.Rank() {
		target.Priority = src.Priority
	}
	if src.Importance.Rank() < target.Importance.Rank() {
		target.Importance = src.Importance
	}
	if src.Deadline != nil && (target.Deadline == nil || src.Deadline.Before(*target.Deadline)) {
		target.Deadline = src.Deadline
	}
	if err := s.tasks.Update(ctx, target); err != nil {
		return nil, err
	}
	now := time.Now()
	src.Status = domain.StatusDone
	src.PrevStatus = ""
	src.SnoozeUntil = nil
	src.RemindAt = nil
	src.MergedInto = target.ID
	src.ClosedAt = &now
	if err := s.tasks.Update(ctx, src); err != nil {
		return nil, err
	}
	s.log.Info("tasks merged", "source_id", src.ID, "target_id", target.ID)
	s.changed(ctx, src)
	s.changed(ctx, target)
	return target, nil
}

// WakeSnoozed restores tasks whose snooze time has come.
func (s *TaskService) WakeSnoozed(ctx context.Context) ([]domain.Task, error) {
	due, err := s.tasks.DueSnoozed(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	woken := make([]domain.Task, 0, len(due))
	for _, t := range due {
		next := t.PrevStatus
		if next != domain.StatusNew && next != domain.StatusInProgress {
			next = domain.StatusNew
		}
		t.Status = next
		t.PrevStatus = ""
		t.SnoozeUntil = nil
		if err := s.tasks.Update(ctx, &t); err != nil {
			s.log.Error("wake snoozed task", "task_id", t.ID, "err", err)
			continue
		}
		s.changed(ctx, &t)
		woken = append(woken, t)
	}
	return woken, nil
}

// SendDraft sends the LLM-generated draft into the source chat.
func (s *TaskService) SendDraft(ctx context.Context, id int64) (*domain.Task, error) {
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(t.DraftReply) == "" {
		return nil, domain.ErrEmptyReply
	}
	return s.SendReply(ctx, id, t.DraftReply)
}

// DoneMessage is the confirmation text offered when closing a task with a notice to the contact.
const DoneMessage = "Готово!"

// sendToContact delivers text to the task's contact: a support desk user via the bot, a Business chat
// on behalf of the owner. It mutates t in memory (ReplySentAt/ReplyText); the caller persists t.
func (s *TaskService) sendToContact(ctx context.Context, t *domain.Task, text string) error {
	if !t.HasChat() {
		return domain.ErrNoSourceChat
	}
	now := time.Now()
	if t.IsHelpdesk() {
		h, ok := s.helpdesks.For(domain.ParseHelpdeskBotID(t.ConnectionID))
		if !ok {
			return domain.ErrHelpdeskOff
		}
		if err := h.ReplyToUser(ctx, t.ChatID, text); err != nil {
			return err
		}
		t.ReplySentAt = &now
		t.ReplyText = text
		return nil
	}
	conn, err := s.conns.Resolve(ctx, t.ConnectionID)
	if err != nil {
		return err
	}
	if !conn.CanReply {
		return domain.ErrCannotReply
	}
	msgID, err := s.sender.SendBusinessText(ctx, conn.ID, t.ChatID, text)
	if err != nil {
		return err
	}
	if _, err := s.messages.Save(ctx, &domain.Message{
		ConnectionID: conn.ID, ChatID: t.ChatID, MessageID: msgID, SenderID: conn.UserID, SenderName: conn.UserName,
		Outgoing: true, Text: text, SentAt: now, Analyzed: true,
	}); err != nil {
		s.log.Warn("store sent reply", "task_id", t.ID, "err", err)
	}
	t.ReplySentAt = &now
	t.ReplyText = text
	return nil
}

// SendReply sends text to the task's contact.
func (s *TaskService) SendReply(ctx context.Context, id int64, text string) (*domain.Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, domain.ErrEmptyReply
	}
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.sendToContact(ctx, t, text); err != nil {
		return nil, err
	}
	if t.Status == domain.StatusNew {
		t.Status = domain.StatusInProgress
	}
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.log.Info("reply sent", "task_id", t.ID, "chat_id", t.ChatID)
	s.changed(ctx, t)
	return t, nil
}

// CloseWithMessage sends text to the contact (typically DoneMessage) and closes the task.
// Used by the "✅ Закрыть" confirmation flow when NotifyDoneOnClose is enabled.
func (s *TaskService) CloseWithMessage(ctx context.Context, id int64, text string) (*domain.Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, domain.ErrEmptyReply
	}
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.sendToContact(ctx, t, text); err != nil {
		return nil, err
	}
	now := time.Now()
	t.Status = domain.StatusDone
	t.PrevStatus = ""
	t.SnoozeUntil = nil
	t.ClosedAt = &now
	if err := s.tasks.Update(ctx, t); err != nil {
		return nil, err
	}
	s.log.Info("task closed with message", "task_id", t.ID, "chat_id", t.ChatID)
	s.changed(ctx, t)
	return t, nil
}

// Digest is a morning summary of hanging and overdue tasks.
type Digest struct {
	Date       time.Time
	Overdue    []domain.Task
	DueSoon    []domain.Task
	Stale      []domain.Task
	New        int
	InProgress int
	Snoozed    int
}

func (d *Digest) Empty() bool { return d.New+d.InProgress+d.Snoozed == 0 }

// BuildDigest collects open tasks of the scope into digest sections. connID, when set, additionally
// narrows to one bot's helpdesk — see domain.TaskFilter.ConnectionID.
func (s *TaskService) BuildDigest(ctx context.Context, scope domain.TaskScope, connID string) (*Digest, error) {
	open, _, err := s.tasks.List(ctx, domain.TaskFilter{
		Statuses:     []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress, domain.StatusSnoozed},
		Scope:        scope,
		ConnectionID: connID,
		Limit:        1000,
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	d := &Digest{Date: now.In(s.settings.Location())}
	for _, t := range open {
		switch t.Status {
		case domain.StatusNew:
			d.New++
		case domain.StatusInProgress:
			d.InProgress++
		case domain.StatusSnoozed:
			d.Snoozed++
			continue
		}
		switch {
		case t.IsOverdue(now):
			d.Overdue = append(d.Overdue, t)
		case t.Deadline != nil && t.Deadline.Sub(now) <= 24*time.Hour:
			d.DueSoon = append(d.DueSoon, t)
		case t.Status == domain.StatusNew && now.Sub(t.CreatedAt) > 24*time.Hour:
			d.Stale = append(d.Stale, t)
		}
	}
	return d, nil
}

// Overview is the main menu summary.
type Overview struct {
	New, InProgress, Snoozed, Overdue int
	Connection                        *domain.BusinessConnection
}

func (s *TaskService) Overview(ctx context.Context, scope domain.TaskScope, connID string) (*Overview, error) {
	counts, err := s.tasks.CountByStatus(ctx, scope, connID, time.Time{})
	if err != nil {
		return nil, err
	}
	overdue, err := s.tasks.CountOverdue(ctx, scope, connID, time.Now())
	if err != nil {
		return nil, err
	}
	o := &Overview{
		New:        counts[domain.StatusNew],
		InProgress: counts[domain.StatusInProgress],
		Snoozed:    counts[domain.StatusSnoozed],
		Overdue:    overdue,
	}
	if c, err := s.conns.Current(ctx); err == nil {
		o.Connection = c
	}
	return o, nil
}

// Stats aggregates triage quality metrics for the period.
type Stats struct {
	Days     int
	Tasks    map[domain.TaskStatus]int
	Analyses *domain.AnalysisStats
}

func (s *TaskService) Stats(ctx context.Context, scope domain.TaskScope, connID string, days int) (*Stats, error) {
	since := time.Now().AddDate(0, 0, -days)
	counts, err := s.tasks.CountByStatus(ctx, scope, connID, since)
	if err != nil {
		return nil, err
	}
	as, err := s.analyses.Stats(ctx, since)
	if err != nil {
		return nil, err
	}
	return &Stats{Days: days, Tasks: counts, Analyses: as}, nil
}
