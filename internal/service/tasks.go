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
	tasks    domain.TaskRepository
	messages domain.MessageRepository
	analyses domain.AnalysisRepository
	conns    *ConnectionService
	settings *SettingsService
	sender   ReplySender
	helpdesk HelpdeskReplier
	observer TaskObserver
	log      *slog.Logger
}

func NewTaskService(tasks domain.TaskRepository, messages domain.MessageRepository, analyses domain.AnalysisRepository,
	conns *ConnectionService, settings *SettingsService, sender ReplySender, log *slog.Logger) *TaskService {
	return &TaskService{tasks: tasks, messages: messages, analyses: analyses, conns: conns, settings: settings,
		sender: sender, log: log.With("component", "tasks")}
}

// SetHelpdesk wires replies to support desk tickets.
func (s *TaskService) SetHelpdesk(h HelpdeskReplier) { s.helpdesk = h }

// SetObserver wires the task change observer (ticket cards in the helpdesk group).
func (s *TaskService) SetObserver(o TaskObserver) { s.observer = o }

func (s *TaskService) changed(ctx context.Context, t *domain.Task) {
	if s.observer == nil {
		return
	}
	cp := *t
	actorCtx := WithActor(context.WithoutCancel(ctx), ActorFrom(ctx))
	go func() {
		ctx, cancel := context.WithTimeout(actorCtx, 30*time.Second)
		defer cancel()
		s.observer.TaskChanged(ctx, &cp)
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
		if s.helpdesk == nil {
			return domain.ErrHelpdeskOff
		}
		if err := s.helpdesk.ReplyToUser(ctx, t.ChatID, text); err != nil {
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

// BuildDigest collects open tasks of the scope into digest sections.
func (s *TaskService) BuildDigest(ctx context.Context, scope domain.TaskScope) (*Digest, error) {
	open, _, err := s.tasks.List(ctx, domain.TaskFilter{
		Statuses: []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress, domain.StatusSnoozed},
		Scope:    scope,
		Limit:    1000,
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

func (s *TaskService) Overview(ctx context.Context, scope domain.TaskScope) (*Overview, error) {
	counts, err := s.tasks.CountByStatus(ctx, scope, time.Time{})
	if err != nil {
		return nil, err
	}
	overdue, err := s.tasks.CountOverdue(ctx, scope, time.Now())
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

func (s *TaskService) Stats(ctx context.Context, scope domain.TaskScope, days int) (*Stats, error) {
	since := time.Now().AddDate(0, 0, -days)
	counts, err := s.tasks.CountByStatus(ctx, scope, since)
	if err != nil {
		return nil, err
	}
	as, err := s.analyses.Stats(ctx, since)
	if err != nil {
		return nil, err
	}
	return &Stats{Days: days, Tasks: counts, Analyses: as}, nil
}
