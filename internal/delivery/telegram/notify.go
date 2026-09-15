package tgbot

import (
	"context"
	"fmt"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
)

// TaskCreated implements service.Notifier. Helpdesk tickets go to the operators' group, personal
// tasks to the owner.
func (b *Bot) TaskCreated(ctx context.Context, t *domain.Task) {
	if t.IsHelpdesk() {
		b.publishTicket(ctx, t)
		return
	}
	header := "🆕 <b>Новая задача</b>"
	if !t.HasChat() {
		header = "🆕 <b>Новая задача из пересланного</b>"
	}
	if err := b.renderTask(ctx, nil, t, header, nil); err != nil {
		b.log.Error("notify task created", "task_id", t.ID, "err", err)
	}
}

// TaskUpdated implements service.Notifier.
func (b *Bot) TaskUpdated(ctx context.Context, t *domain.Task) {
	if t.IsHelpdesk() {
		b.publishTicket(ctx, t)
		b.topicNotice(ctx, t, fmt.Sprintf("🔄 Тикет #%d дополнен новыми сообщениями", t.ID))
		return
	}
	if err := b.renderTask(ctx, nil, t, "🔄 <b>Задача дополнена новыми сообщениями</b>", nil); err != nil {
		b.log.Error("notify task updated", "task_id", t.ID, "err", err)
	}
}

// TaskChanged implements service.TaskObserver: keeps ticket cards in the group up to date.
func (b *Bot) TaskChanged(ctx context.Context, t *domain.Task) {
	if t.IsHelpdesk() {
		b.publishTicket(ctx, t)
	}
}

// AnalysisFailed implements service.Notifier.
func (b *Bot) AnalysisFailed(ctx context.Context, rec *domain.AnalysisRecord, contactName string) {
	where := ""
	if rec.ConnectionID == domain.HelpdeskConnectionID {
		where = " (хелпдеск)"
	}
	text := fmt.Sprintf("⚠️ <b>Не удалось проанализировать сообщения</b> от %s%s\n\n"+
		"🤖 %s · <code>%s</code>\n<code>%s</code>\n\n💬 <blockquote expandable>%s</blockquote>",
		esc(contactName), where, providerTitle(rec.Provider), esc(rec.Model), esc(trunc(rec.Error, 400)), esc(trunc(rec.InputText, 1500)))
	markup := kb(row(cb("🔁 Повторить анализ", fmt.Sprintf("ar:%d", rec.ID))), row(cb("⚙️ Настройки", "st")))
	if err := b.sendText(ctx, text, markup); err != nil {
		b.log.Error("notify analysis failure", "err", err)
	}
}

// ForwardFailed implements service.Notifier. Forwards are not stored, so there is nothing to retry
// from the bot — the owner forwards the messages again.
func (b *Bot) ForwardFailed(ctx context.Context, rec *domain.AnalysisRecord) {
	text := fmt.Sprintf("⚠️ <b>Не удалось создать задачу из пересланного</b>\n\n"+
		"🤖 %s · <code>%s</code>\n<code>%s</code>\n\n💬 <blockquote expandable>%s</blockquote>\n\nПерешлите сообщения ещё раз.",
		providerTitle(rec.Provider), esc(rec.Model), esc(trunc(rec.Error, 400)), esc(trunc(rec.InputText, 1500)))
	if err := b.sendText(ctx, text, kb(row(cb("⚙️ Настройки", "st")))); err != nil {
		b.log.Error("notify forward failure", "err", err)
	}
}

// SnoozeFired implements service.SchedulerNotifier.
func (b *Bot) SnoozeFired(ctx context.Context, t *domain.Task) {
	if t.IsHelpdesk() {
		b.topicNotice(ctx, t, fmt.Sprintf("⏰ <b>Напоминание по тикету #%d</b> · %s", t.ID, esc(trunc(t.Title, 100))))
		return
	}
	if err := b.renderTask(ctx, nil, t, "⏰ <b>Напоминание об отложенной задаче</b>", nil); err != nil {
		b.log.Error("notify snooze", "task_id", t.ID, "err", err)
	}
}

// Digest implements service.SchedulerNotifier.
func (b *Bot) Digest(ctx context.Context, d *service.Digest) {
	text, markup := b.digestView(d)
	if err := b.sendText(ctx, text, markup); err != nil {
		b.log.Error("send digest", "err", err)
	}
}
