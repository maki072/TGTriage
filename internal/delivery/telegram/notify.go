package tgbot

import (
	"context"
	"fmt"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
)

// TaskCreated implements service.Notifier.
func (b *Bot) TaskCreated(ctx context.Context, t *domain.Task) {
	if err := b.renderTask(ctx, nil, t, "🆕 <b>Новая задача</b>", nil); err != nil {
		b.log.Error("notify task created", "task_id", t.ID, "err", err)
	}
}

// TaskUpdated implements service.Notifier.
func (b *Bot) TaskUpdated(ctx context.Context, t *domain.Task) {
	if err := b.renderTask(ctx, nil, t, "🔄 <b>Задача дополнена новыми сообщениями</b>", nil); err != nil {
		b.log.Error("notify task updated", "task_id", t.ID, "err", err)
	}
}

// AnalysisFailed implements service.Notifier.
func (b *Bot) AnalysisFailed(ctx context.Context, rec *domain.AnalysisRecord, contactName string) {
	text := fmt.Sprintf("⚠️ <b>Не удалось проанализировать сообщения</b> от %s\n\n"+
		"🤖 %s · <code>%s</code>\n<code>%s</code>\n\n💬 <blockquote expandable>%s</blockquote>",
		esc(contactName), providerTitle(rec.Provider), esc(rec.Model), esc(trunc(rec.Error, 400)), esc(trunc(rec.InputText, 1500)))
	markup := kb(row(cb("🔁 Повторить анализ", fmt.Sprintf("ar:%d", rec.ID))), row(cb("⚙️ Настройки", "st")))
	if err := b.sendText(ctx, text, markup); err != nil {
		b.log.Error("notify analysis failure", "err", err)
	}
}

// SnoozeFired implements service.SchedulerNotifier.
func (b *Bot) SnoozeFired(ctx context.Context, t *domain.Task) {
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
