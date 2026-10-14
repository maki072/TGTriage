package tgbot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// autoCloseOnDone closes the open task of a Business chat when the owner writes a short "готово" /
// "сделал" / "готово, отключился" in that chat. One open task is closed and reported with an undo
// button; several open tasks are never guessed at — the owner is asked which one is meant.
func (b *Bot) autoCloseOnDone(ctx context.Context, connID string, m *domain.Message) {
	if !b.settings.Get().AutoCloseOnDone || !service.IsDoneMessage(m.Text) {
		return
	}
	open, _, err := b.tasks.List(ctx, domain.TaskFilter{
		Statuses: []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress, domain.StatusSnoozed},
		ChatID:   m.ChatID, ConnectionID: connID, Limit: 10,
	})
	if err != nil {
		b.log.Warn("auto-close: list open tasks", "chat_id", m.ChatID, "err", err)
		return
	}
	// a task created after the message cannot be the one it answers
	var cands []domain.Task
	for _, t := range open {
		if !t.CreatedAt.After(m.SentAt.Add(time.Minute)) {
			cands = append(cands, t)
		}
	}
	quote := "«" + esc(trunc(m.Text, 60)) + "»"
	switch len(cands) {
	case 0:
		return
	case 1:
		t, err := b.tasks.SetStatus(ctx, cands[0].ID, domain.StatusDone)
		if err != nil {
			b.log.Warn("auto-close: close task", "task_id", cands[0].ID, "err", err)
			return
		}
		b.log.Info("task closed by the owner's message", "task_id", t.ID, "chat_id", m.ChatID)
		text := fmt.Sprintf("<b>Задача #%d закрыта</b> — вы написали %s\n%s\n<i>%s</i>", t.ID, quote, esc(trunc(t.Title, 120)), esc(trunc(t.SenderName, 40)))
		markup := kb(row(cb("Вернуть в работу", fmt.Sprintf("ta:reopen:%d", t.ID)), cb("Открыть", fmt.Sprintf("tv:%d", t.ID))))
		if err := b.sendText(ctx, text, markup); err != nil {
			b.log.Warn("auto-close: notify", "err", err)
		}
	default:
		var rows [][]button
		for _, t := range cands {
			rows = append(rows, row(cb(fmt.Sprintf("Закрыть #%d %s", t.ID, trunc(t.Title, 36)), fmt.Sprintf("ta:done:%d", t.ID))))
		}
		name := strings.TrimSpace(cands[0].SenderName)
		text := fmt.Sprintf("Вы написали %s в чате с %s, а открытых задач несколько. Какую закрыть?", quote, esc(trunc(name, 40)))
		if err := b.sendText(ctx, text, kb(rows...)); err != nil {
			b.log.Warn("auto-close: ask which task", "err", err)
		}
	}
}

// autoCloseTicketOnDone closes the open ticket of a helpdesk user when an operator writes a short
// "готово" / "готово, отключился" in the user's topic. Several open tickets are never guessed at:
// the operators are told to close the right one by hand.
func (b *Bot) autoCloseTicketOnDone(ctx context.Context, in service.OperatorMessage) {
	if service.IsInternalNote(in.RawText) {
		return
	}
	u, err := b.helpdesk.TopicUser(ctx, in.GroupID, in.TopicID)
	if err != nil {
		return
	}
	t, ambiguous, err := b.tasks.CloseHelpdeskOnDone(ctx, b.cfg.BotDBID, u.UserID, in.Text, in.Date)
	switch {
	case err != nil:
		b.log.Warn("auto-close ticket", "user_id", u.UserID, "err", err)
	case t != nil:
		b.log.Info("ticket closed by the operator's message", "task_id", t.ID, "user_id", u.UserID)
		b.topicNotice(ctx, t, fmt.Sprintf("✅ <b>Тикет #%d закрыт</b> — оператор написал «%s»", t.ID, esc(trunc(in.Text, 60))))
	case ambiguous:
		if _, serr := b.api.SendMessage(ctx, telegram.SendMessageParams{ChatID: in.GroupID, MessageThreadID: in.TopicID,
			Text: "Открытых тикетов несколько — закройте нужный кнопкой на карточке.", DisableNotification: true}); serr != nil {
			b.log.Debug("post auto-close hint", "err", serr)
		}
	}
}
