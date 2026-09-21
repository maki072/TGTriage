package tgbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// Callback data scheme (<= 64 bytes):
//   m                      main menu
//   tl:<status>:<prio>:<p> task list
//   tv:<id>                task card
//   ta:<action>:<id>       task action: draft|reply|work|done|fp|reopen|snz
//   ts:<id>:<opt>          snooze: minutes|tm (tomorrow 09:00)|c (custom)
//   tc:<id>:<y|n>          confirm close-with-message: y sends DoneMessage, n closes silently
//   st, sm:<prov>, smp:<prov>:<i>, smc:<prov>, sd:<sec|c>, ss:<sens>,
//   sg:<t|c>, stp, smr, sfd, stest, sreset[:y]   settings
//   dg digest, sx stats, ar:<analysis_id> retry analysis, cx cancel input, noop

func (b *Bot) onCallback(ctx context.Context, q *telegram.CallbackQuery) {
	answered := false
	answer := func(text string, alert bool) {
		if answered {
			return
		}
		answered = true
		if err := b.api.AnswerCallbackQuery(ctx, q.ID, trunc(text, 190), alert); err != nil {
			b.log.Debug("answerCallbackQuery failed", "err", err)
		}
	}
	defer answer("", false)

	ctx = service.WithActor(ctx, service.Actor{ID: q.From.ID, Name: q.From.FullName()})
	parts := strings.Split(q.Data, ":")
	if len(parts) == 1 && parts[0] == captchaCallback && q.From.ID != b.cfg.OwnerID && q.Message != nil && q.Message.Chat.Type == "private" {
		b.captchaPassed(ctx, q, answer)
		return
	}
	if q.Message != nil && q.Message.Chat.ID != 0 && q.Message.Chat.ID == b.helpdesk.GroupID() {
		// ticket cards in the helpdesk group: every member of the group is an operator
		if err := b.groupCallback(ctx, groupRef(q), parts, answer); err != nil {
			b.log.Warn("group callback failed", "data", q.Data, "err", err)
			answer("Ошибка: "+humanError(err), true)
		}
		return
	}
	if q.From.ID != b.cfg.OwnerID {
		answer("Нет доступа", true)
		return
	}
	var ref *msgRef
	if q.Message != nil && q.Message.Date != 0 {
		ref = &msgRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID}
	}
	if err := b.routeCallback(ctx, ref, parts, answer); err != nil {
		b.log.Warn("callback failed", "data", q.Data, "err", err)
		answer("Ошибка: "+humanError(err), true)
	}
}

func (b *Bot) routeCallback(ctx context.Context, ref *msgRef, p []string, answer func(string, bool)) error {
	arg := func(i int) string {
		if i < len(p) {
			return p[i]
		}
		return ""
	}
	num := func(i int) int64 {
		n, _ := strconv.ParseInt(arg(i), 10, 64)
		return n
	}

	switch p[0] {
	case "noop":
		return nil
	case "m":
		b.states.clear()
		return b.showMainMenu(ctx, ref)
	case "tl":
		return b.showList(ctx, ref, parseListQuery(p[1:]))
	case "tv":
		return b.showTask(ctx, ref, num(1))
	case "ta":
		return b.taskAction(ctx, ref, arg(1), num(2), answer)
	case "ts":
		return b.snoozeAction(ctx, ref, num(1), arg(2), answer)
	case "tc":
		return b.closeAction(ctx, ref, num(1), arg(2), answer)

	case "st":
		b.states.clear()
		return b.showSettings(ctx, ref)
	case "sa":
		return b.showSettingsMore(ctx, ref)
	case "sm":
		return b.showModelMenu(ctx, ref, arg(1))
	case "smp":
		provider := arg(1)
		presets := b.presets(provider)
		idx := int(num(2))
		if idx < 0 || idx >= len(presets) {
			return domain.ErrInvalidInput
		}
		if err := b.setModel(ctx, provider, presets[idx]); err != nil {
			return err
		}
		answer("Модель: "+presets[idx], false)
		return b.showSettingsMore(ctx, ref)
	case "smc":
		provider := arg(1)
		b.states.set(dialogState{Kind: stateModel, Provider: provider})
		return b.render(ctx, ref, fmt.Sprintf("Отправьте идентификатор модели %s, например <code>%s</code>",
			providerTitle(provider), esc(b.settings.Get().ModelFor(provider))), kb(row(cb("Отмена", "st"))))
	case "sd":
		if arg(1) == "c" {
			b.states.set(dialogState{Kind: stateDebounce})
			return b.render(ctx, ref, "Отправьте время дебаунса в секундах (1–600).", kb(row(cb("Отмена", "st"))))
		}
		sec := int(num(1))
		if _, err := b.settings.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = sec }); err != nil {
			return err
		}
		answer(fmt.Sprintf("Дебаунс: %d с", sec), false)
		return b.showSettings(ctx, ref)
	case "ss":
		sens, ok := domain.ParseSensitivity(arg(1))
		if !ok {
			return domain.ErrInvalidInput
		}
		if _, err := b.settings.Update(ctx, func(s *domain.Settings) { s.Sensitivity = sens }); err != nil {
			return err
		}
		answer("Чувствительность: "+sensitivityName(sens), false)
		return b.showSettings(ctx, ref)
	case "sg":
		if arg(1) == "c" {
			b.states.set(dialogState{Kind: stateDigestTime})
			return b.render(ctx, ref, "Отправьте время дайджеста в формате <code>ЧЧ:ММ</code>, например <code>08:30</code>.",
				kb(row(cb("Отмена", "st"))))
		}
		st, err := b.settings.Update(ctx, func(s *domain.Settings) { s.DigestEnabled = !s.DigestEnabled })
		if err != nil {
			return err
		}
		answer("Дайджест: "+yesNo(st.DigestEnabled), false)
		return b.showSettings(ctx, ref)
	case "stp":
		st, err := b.settings.Update(ctx, func(s *domain.Settings) { s.TriagePaused = !s.TriagePaused })
		if err != nil {
			return err
		}
		if st.TriagePaused {
			answer("Личный триаж приостановлен (хелпдеск работает)", false)
		} else {
			answer("Личный триаж возобновлён", false)
		}
		return b.showSettings(ctx, ref)
	case "smr":
		if _, err := b.settings.Update(ctx, func(s *domain.Settings) { s.MarkReadOnWork = !s.MarkReadOnWork }); err != nil {
			return err
		}
		return b.showSettingsMore(ctx, ref)
	case "sfd":
		if _, err := b.settings.Update(ctx, func(s *domain.Settings) { s.NotifyDoneOnClose = !s.NotifyDoneOnClose }); err != nil {
			return err
		}
		return b.showSettingsMore(ctx, ref)
	case "sac":
		if _, err := b.settings.Update(ctx, func(s *domain.Settings) { s.AutoCloseOnDone = !s.AutoCloseOnDone }); err != nil {
			return err
		}
		return b.showSettingsMore(ctx, ref)
	case "stest":
		answer("Проверяю провайдера…", false)
		go b.probe(ctx)
		return nil
	case "sreset":
		if arg(1) == "y" {
			if err := b.settings.Reset(ctx); err != nil {
				return err
			}
			answer("Настройки сброшены к значениям из окружения", false)
			return b.showSettings(ctx, ref)
		}
		return b.render(ctx, ref, "Сбросить все настройки к значениям из переменных окружения?",
			kb(row(cb("Да, сбросить", "sreset:y"), cb("Отмена", "st"))))

	case "dg":
		answer("", false)
		return b.sendDigest(ctx)
	case "sx":
		return b.showStats(ctx, ref)
	case "ar":
		if err := b.triage.Retry(ctx, num(1)); err != nil {
			return err
		}
		answer("Анализ поставлен в очередь", false)
		return b.render(ctx, ref, "Повторный анализ запущен — результат придёт отдельным сообщением.", nil)
	case "hg":
		return b.useHelpdeskGroup(ctx, ref, num(1), answer)
	case "cx":
		b.states.clear()
		answer("Отменено", false)
		return b.render(ctx, ref, "Ввод отменён", kb(row(cb("Меню", "m"))))
	}
	return nil
}

func (b *Bot) setModel(ctx context.Context, provider, model string) error {
	if !domainProvider(provider) {
		return domain.ErrInvalidInput
	}
	_, err := b.settings.Update(ctx, func(s *domain.Settings) { s.Provider(provider).Model = model })
	return err
}

func domainProvider(p string) bool {
	switch p {
	case domain.ProviderClaude, domain.ProviderGemini, domain.ProviderGroq, domain.ProviderMistral, domain.ProviderOpenRouter:
		return true
	}
	return false
}

func (b *Bot) taskAction(ctx context.Context, ref *msgRef, action string, id int64, answer func(string, bool)) error {
	setStatus := func(status domain.TaskStatus, notice string) error {
		t, err := b.tasks.SetStatus(ctx, id, status)
		if err != nil {
			return err
		}
		answer(notice, false)
		return b.renderTask(ctx, ref, t, "", nil)
	}
	switch action {
	case "draft":
		t, err := b.tasks.SendDraft(ctx, id)
		if err != nil {
			return err
		}
		answer("Черновик отправлен собеседнику", false)
		return b.renderTask(ctx, ref, t, "", nil)
	case "reply":
		t, err := b.tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		b.states.set(dialogState{Kind: stateReply, TaskID: id})
		answer("Жду текст ответа", false)
		return b.sendText(ctx, fmt.Sprintf("Напишите ответ для <b>%s</b> (задача #%d).\n"+
			"Следующее ваше сообщение будет отправлено собеседнику от вашего имени.", esc(t.SenderName), t.ID),
			kb(row(cb("Отмена", "cx"))))
	case "work":
		return setStatus(domain.StatusInProgress, "Взято в работу")
	case "done":
		t, err := b.tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		if !b.settings.Get().NotifyDoneOnClose || !t.HasChat() {
			return setStatus(domain.StatusDone, "Задача закрыта")
		}
		return b.renderTask(ctx, ref, t, "<b>Закрыть задачу?</b>", closeConfirmKeyboard(id))
	case "fp":
		return setStatus(domain.StatusFalsePositive, "Отмечено: не задача")
	case "reopen":
		return setStatus(domain.StatusNew, "Задача возвращена")
	case "more":
		t, err := b.tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		return b.renderTask(ctx, ref, t, "<b>Другие действия</b>", moreKeyboard(t))
	case "snz":
		t, err := b.tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		return b.renderTask(ctx, ref, t, "<b>На сколько отложить?</b>", snoozeKeyboard(id))
	}
	return domain.ErrInvalidInput
}

func (b *Bot) snoozeAction(ctx context.Context, ref *msgRef, id int64, opt string, answer func(string, bool)) error {
	loc := b.settings.Location()
	now := time.Now().In(loc)
	var until time.Time
	switch opt {
	case "c":
		b.states.set(dialogState{Kind: stateSnoozeCustom, TaskID: id})
		answer("Жду срок", false)
		return b.sendText(ctx, "Отправьте срок для задачи #"+strconv.FormatInt(id, 10)+".\n"+
			"Примеры: <code>45m</code>, <code>2h</code>, <code>1d</code>, <code>18:00</code>, <code>завтра 10:00</code>, <code>20.09 12:00</code>",
			kb(row(cb("Отмена", "cx"))))
	case "tm":
		d := now.AddDate(0, 0, 1)
		until = time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, loc)
	default:
		minutes, err := strconv.Atoi(opt)
		if err != nil || minutes <= 0 {
			return domain.ErrInvalidInput
		}
		until = now.Add(time.Duration(minutes) * time.Minute)
	}
	t, err := b.tasks.Snooze(ctx, id, until)
	if err != nil {
		return err
	}
	answer("Отложено до "+b.fmtShort(until), false)
	return b.renderTask(ctx, ref, t, "", nil)
}

// closeAction handles the close-confirmation keyboard shown when NotifyDoneOnClose is enabled.
func (b *Bot) closeAction(ctx context.Context, ref *msgRef, id int64, opt string, answer func(string, bool)) error {
	switch opt {
	case "y":
		t, err := b.tasks.CloseWithMessage(ctx, id, service.DoneMessage)
		if err != nil {
			return err
		}
		answer("Закрыто, собеседнику отправлено «"+service.DoneMessage+"»", false)
		return b.renderTask(ctx, ref, t, "", nil)
	case "n":
		t, err := b.tasks.SetStatus(ctx, id, domain.StatusDone)
		if err != nil {
			return err
		}
		answer("Задача закрыта без сообщения", false)
		return b.renderTask(ctx, ref, t, "", nil)
	}
	return domain.ErrInvalidInput
}

func (b *Bot) probe(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Minute)
	defer cancel()
	results, err := b.triage.Probe(ctx)
	var sb strings.Builder
	sb.WriteString("<b>Проверка ключей AI</b>\n\n")
	if err != nil {
		fmt.Fprintf(&sb, "Ошибка: <code>%s</code>", esc(humanError(err)))
	}
	for _, r := range results {
		fmt.Fprintf(&sb, "%d. <b>%s</b> · <code>%s</code> · <code>%s</code> · %.1f с\n",
			r.Index, providerTitle(r.Provider), esc(r.KeyMask), esc(r.Model), r.Latency.Seconds())
		if r.Err != nil {
			fmt.Fprintf(&sb, "Ошибка: <code>%s</code>\n\n", esc(trunc(humanError(r.Err), 300)))
			continue
		}
		fmt.Fprintf(&sb, "Работает · задача: %s · уверенность %.0f%%\n\n", yesNo(r.Analysis.IsTask), r.Analysis.Confidence*100)
	}
	if err := b.sendText(ctx, sb.String(), kb(row(cb("Настройки", "st")))); err != nil {
		b.log.Warn("send probe result", "err", err)
	}
}

// groupRef identifies the message a callback came from (nil for inaccessible messages).
func groupRef(q *telegram.CallbackQuery) *msgRef {
	if q.Message == nil || q.Message.Chat.ID == 0 {
		return nil
	}
	return &msgRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID}
}
