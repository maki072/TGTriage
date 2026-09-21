package tgbot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// The bot follows the same rules as the Mini App (see the TG Triage design system): one leading
// action per card, rare actions behind "Ещё", filters reduced to a status row plus one priority
// toggle, no emoji — the priority is drawn as four bars, statuses and problems are spelled out.

const pageSize = 6

// ---------- task list ----------

type listQuery struct {
	Status string
	Prio   string
	Page   int
}

func defaultListQuery() listQuery { return listQuery{Status: "act", Prio: "all"} }

func (q listQuery) data() string { return fmt.Sprintf("tl:%s:%s:%d", q.Status, q.Prio, q.Page) }

type statusFilter struct {
	key, label string
	statuses   []domain.TaskStatus
}

var statusFilters = []statusFilter{
	{"act", "Активные", []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress}},
	{"new", "Новые", []domain.TaskStatus{domain.StatusNew}},
	{"wrk", "В работе", []domain.TaskStatus{domain.StatusInProgress}},
	{"snz", "Отложенные", []domain.TaskStatus{domain.StatusSnoozed}},
	{"done", "Завершённые", []domain.TaskStatus{domain.StatusDone}},
	{"fp", "Не задачи", []domain.TaskStatus{domain.StatusFalsePositive}},
	{"all", "Все", nil},
}

// historyStatus reports whether a status filter belongs to the history view (finished tasks).
func historyStatus(key string) bool { return key == "done" || key == "fp" || key == "all" }

type prioFilter struct {
	key, title string
	prios      []domain.Priority
}

// prioFilters is a cycle: one button steps through it, so every priority view stays reachable
// without a row of six buttons.
var prioFilters = []prioFilter{
	{"all", "любой", nil},
	{"urg", "срочные", []domain.Priority{domain.PriorityCritical, domain.PriorityHigh}},
	{"crit", "критические", []domain.Priority{domain.PriorityCritical}},
	{"high", "высокие", []domain.Priority{domain.PriorityHigh}},
	{"med", "средние", []domain.Priority{domain.PriorityMedium}},
	{"low", "низкие", []domain.Priority{domain.PriorityLow}},
}

func findStatusFilter(key string) (statusFilter, bool) {
	for _, f := range statusFilters {
		if f.key == key {
			return f, true
		}
	}
	return statusFilters[0], false
}

func findPrioFilter(key string) (prioFilter, bool) {
	for _, f := range prioFilters {
		if f.key == key {
			return f, true
		}
	}
	return prioFilters[0], false
}

func nextPrioFilter(key string) prioFilter {
	for i, f := range prioFilters {
		if f.key == key {
			return prioFilters[(i+1)%len(prioFilters)]
		}
	}
	return prioFilters[0]
}

func parseListQuery(parts []string) listQuery {
	q := defaultListQuery()
	if len(parts) > 0 {
		if _, ok := findStatusFilter(parts[0]); ok {
			q.Status = parts[0]
		}
	}
	if len(parts) > 1 {
		if _, ok := findPrioFilter(parts[1]); ok {
			q.Prio = parts[1]
		}
	}
	if len(parts) > 2 {
		if n, err := strconv.Atoi(parts[2]); err == nil && n >= 0 {
			q.Page = n
		}
	}
	return q
}

func (b *Bot) showList(ctx context.Context, ref *msgRef, q listQuery) error {
	sf, _ := findStatusFilter(q.Status)
	pf, _ := findPrioFilter(q.Prio)
	f := domain.TaskFilter{Statuses: sf.statuses, Priorities: pf.prios, Limit: pageSize, Offset: q.Page * pageSize}
	items, total, err := b.tasks.List(ctx, f)
	if err != nil {
		return b.renderError(ctx, ref, err)
	}
	if total > 0 && len(items) == 0 && q.Page > 0 {
		q.Page = (total - 1) / pageSize
		f.Offset = q.Page * pageSize
		if items, total, err = b.tasks.List(ctx, f); err != nil {
			return b.renderError(ctx, ref, err)
		}
	}
	b.states.setList(q)
	pages := max(1, (total+pageSize-1)/pageSize)

	var sb strings.Builder
	title := "Задачи"
	if historyStatus(q.Status) {
		title = "История"
	}
	fmt.Fprintf(&sb, "<b>%s</b> · %s", title, sf.label)
	if pf.key != "all" {
		sb.WriteString(" · " + pf.title)
	}
	if pages > 1 {
		fmt.Fprintf(&sb, "\nНайдено: <b>%d</b> · стр. %d из %d\n", total, q.Page+1, pages)
	} else {
		fmt.Fprintf(&sb, "\nНайдено: <b>%d</b>\n", total)
	}
	if total == 0 {
		sb.WriteString("\n<i>Здесь пусто.</i>")
	}

	now := time.Now()
	var rows [][]button
	for _, t := range items {
		fmt.Fprintf(&sb, "\n<b>#%d</b> %s\n", t.ID, esc(trunc(t.Title, 80)))
		meta := []string{priorityBars(t.Priority) + " " + esc(trunc(t.SenderName, 30)), categoryLabel(t.Category)}
		if len(sf.statuses) != 1 {
			meta = append(meta, statusLabel(t.Status))
		}
		if t.Deadline != nil {
			d := "до " + b.fmtShort(*t.Deadline)
			if t.IsOverdue(now) {
				d = "<b>просрочено</b> · " + b.fmtShort(*t.Deadline)
			}
			meta = append(meta, d)
		}
		if t.Status == domain.StatusSnoozed && t.SnoozeUntil != nil {
			meta = append(meta, "отложена до "+b.fmtShort(*t.SnoozeUntil))
		}
		fmt.Fprintf(&sb, "%s\n", strings.Join(meta, " · "))
		rows = append(rows, row(cb(fmt.Sprintf("#%d %s", t.ID, trunc(t.Title, 44)), fmt.Sprintf("tv:%d", t.ID))))
	}

	if pages > 1 {
		var nav []button
		if q.Page > 0 {
			nav = append(nav, cb("‹ Назад", listQuery{q.Status, q.Prio, q.Page - 1}.data()))
		}
		nav = append(nav, cb(fmt.Sprintf("%d / %d", q.Page+1, pages), "noop"))
		if q.Page+1 < pages {
			nav = append(nav, cb("Вперёд ›", listQuery{q.Status, q.Prio, q.Page + 1}.data()))
		}
		rows = append(rows, nav)
	}
	// One status row (active view) or history row, plus a single priority button that cycles.
	var status []button
	if historyStatus(q.Status) {
		for _, k := range []string{"done", "fp", "all"} {
			s, _ := findStatusFilter(k)
			status = append(status, cb(mark(s.key == q.Status, s.label), listQuery{s.key, q.Prio, 0}.data()))
		}
	} else {
		for _, k := range []string{"act", "new", "wrk", "snz"} {
			s, _ := findStatusFilter(k)
			status = append(status, cb(mark(s.key == q.Status, s.label), listQuery{s.key, q.Prio, 0}.data()))
		}
	}
	rows = append(rows, status)
	switchView := cb("История", listQuery{"done", q.Prio, 0}.data())
	if historyStatus(q.Status) {
		switchView = cb("К активным", listQuery{"act", q.Prio, 0}.data())
	}
	rows = append(rows, row(cb("Приоритет: "+pf.title, listQuery{q.Status, nextPrioFilter(q.Prio).key, 0}.data()), switchView))
	rows = append(rows, row(cb("Обновить", q.data()), cb("Меню", "m")))
	return b.render(ctx, ref, sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ---------- task card ----------

func (b *Bot) showTask(ctx context.Context, ref *msgRef, id int64) error {
	t, err := b.tasks.Get(ctx, id)
	if err != nil {
		return b.renderError(ctx, ref, err)
	}
	return b.renderTask(ctx, ref, t, "", nil)
}

// renderTask renders a task card; markup nil means the default action keyboard.
// If Telegram rejects deep links, the card is re-rendered without them.
func (b *Bot) renderTask(ctx context.Context, ref *msgRef, t *domain.Task, header string, markup *telegram.InlineKeyboardMarkup) error {
	if markup == nil {
		markup = b.taskKeyboard(t)
	}
	links := b.settings.Get().DeepLinks
	err := b.render(ctx, ref, b.taskCardText(t, header, links), markup)
	if err != nil && links && telegram.IsEntityError(err) {
		b.log.Warn("task card rejected with links, retrying without them", "err", err)
		err = b.render(ctx, ref, b.taskCardText(t, header, false), markup)
	}
	return err
}

func (b *Bot) taskCardText(t *domain.Task, header string, links bool) string {
	now := time.Now()
	var sb strings.Builder
	if header != "" {
		sb.WriteString(header + "\n\n")
	}
	kind := "Задача"
	if t.IsHelpdesk() {
		kind = "Тикет"
	}
	fmt.Fprintf(&sb, "<b>%s #%d</b> · %s %s\n", kind, t.ID, priorityBars(t.Priority), priorityName(t.Priority))
	fmt.Fprintf(&sb, "<b>%s</b>\n", esc(trunc(t.Title, 150)))
	status := statusLabel(t.Status)
	if t.Status == domain.StatusSnoozed && t.SnoozeUntil != nil {
		status += " до " + b.fmtShort(*t.SnoozeUntil)
	}
	fmt.Fprintf(&sb, "%s · %s\n\n", status, categoryLabel(t.Category))

	sender := esc(trunc(t.SenderName, 60))
	if links && (t.SenderUsername != "" || t.SenderID != 0) {
		sender = fmt.Sprintf(`<a href="%s">%s</a>`, esc(profileURL(t.SenderUsername, t.SenderID)), sender)
	}
	if t.SenderUsername != "" {
		sender += " (@" + esc(t.SenderUsername) + ")"
	}
	switch {
	case t.IsHelpdesk():
		fmt.Fprintf(&sb, "Пользователь: %s\n", esc(trunc(t.SenderName, 60)))
		if u := b.ticketUser(context.Background(), t); u != nil {
			if link := b.helpdesk.TopicURL(u); link != "" {
				fmt.Fprintf(&sb, "<a href=\"%s\">Тема пользователя</a>\n", esc(link))
			}
		}
	case t.HasChat():
		fmt.Fprintf(&sb, "От: %s\n", sender)
	default:
		fmt.Fprintf(&sb, "Переслано, автор: %s\n", sender)
	}
	if links && t.FirstSourceMessageID() > 0 && !t.IsHelpdesk() {
		fmt.Fprintf(&sb, "<a href=\"%s\">Открыть исходное сообщение</a>\n", esc(messageURL(t.ChatID, t.FirstSourceMessageID())))
	}
	if t.Deadline != nil {
		d := b.fmtTime(*t.Deadline)
		if t.IsOverdue(now) {
			d += " · <b>просрочено</b>"
		}
		fmt.Fprintf(&sb, "Срок: %s\n", d)
	}
	fmt.Fprintf(&sb, "Получено: %s\n", b.fmtTime(t.CreatedAt))

	if t.Description != "" {
		fmt.Fprintf(&sb, "\n<b>Суть</b>\n%s\n", esc(trunc(t.Description, 1200)))
	}
	if t.SourceText != "" {
		fmt.Fprintf(&sb, "\n<b>Исходный текст</b>\n<blockquote expandable>%s</blockquote>\n", esc(trunc(t.SourceText, 1500)))
	}
	if t.DraftReply != "" && t.ReplySentAt == nil {
		fmt.Fprintf(&sb, "\n<b>Черновик ответа</b> · %s\n<i>%s</i>\n", strategyLabel(t.ReplyStrategy), esc(trunc(t.DraftReply, 800)))
	}
	if t.ReplySentAt != nil {
		fmt.Fprintf(&sb, "\n<b>Ответ отправлен</b> · %s\n<i>%s</i>\n", b.fmtShort(*t.ReplySentAt), esc(trunc(t.ReplyText, 600)))
	}
	if t.Provider != "" {
		fmt.Fprintf(&sb, "\n<i>%s · %s · уверенность %.0f%%</i>", providerTitle(t.Provider), esc(t.Model), t.Confidence*100)
	}
	return sb.String()
}

// taskKeyboard is the whole action set of a card in at most two action rows: the leading
// action(s) to answer, then "Закрыть" and "Ещё" (work / snooze / not-a-task live behind it).
func (b *Bot) taskKeyboard(t *domain.Task) *telegram.InlineKeyboardMarkup {
	id := t.ID
	data := func(action string) string { return fmt.Sprintf("ta:%s:%d", action, id) }
	var rows [][]button
	if t.Status.IsOpen() {
		if t.HasChat() {
			if t.DraftReply != "" {
				label := "Отправить черновик"
				if t.ReplySentAt != nil {
					label = "Отправить черновик ещё раз"
				}
				rows = append(rows, row(cb(label, data("draft")), cb("Свой ответ", data("reply"))))
			} else {
				rows = append(rows, row(cb("Ответить", data("reply"))))
			}
		}
		rows = append(rows, row(cb("Закрыть", data("done")), cb("Ещё", data("more"))))
	} else {
		r := row(cb("Вернуть в работу", data("reopen")))
		if t.HasChat() {
			r = append(r, cb("Написать", data("reply")))
		}
		rows = append(rows, r)
	}
	rows = append(rows, row(cb("‹ К списку", b.states.list().data()), cb("Меню", "m")))
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// moreKeyboard holds the rarer actions; the destructive one comes last.
func moreKeyboard(t *domain.Task) *telegram.InlineKeyboardMarkup {
	data := func(action string) string { return fmt.Sprintf("ta:%s:%d", action, t.ID) }
	var rows [][]button
	if t.Status != domain.StatusInProgress {
		rows = append(rows, row(cb("Взять в работу", data("work"))))
	}
	rows = append(rows, row(cb("Отложить…", data("snz"))), row(cb("Не задача", data("fp"))), row(cb("‹ Назад", fmt.Sprintf("tv:%d", t.ID))))
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func snoozeKeyboard(id int64) *telegram.InlineKeyboardMarkup {
	d := func(v string) string { return fmt.Sprintf("ts:%d:%s", id, v) }
	return kb(
		row(cb("30 мин", d("30")), cb("1 час", d("60")), cb("3 часа", d("180"))),
		row(cb("Завтра 09:00", d("tm")), cb("3 дня", d("4320")), cb("Неделя", d("10080"))),
		row(cb("Свой срок", d("c"))),
		row(cb("‹ Назад", fmt.Sprintf("ta:more:%d", id))),
	)
}

func closeConfirmKeyboard(id int64) *telegram.InlineKeyboardMarkup {
	return kb(
		row(cb("Закрыть и отправить «Готово»", fmt.Sprintf("tc:%d:y", id))),
		row(cb("Закрыть без сообщения", fmt.Sprintf("tc:%d:n", id))),
		row(cb("‹ Назад", fmt.Sprintf("tv:%d", id))),
	)
}

// ---------- main menu ----------

func (b *Bot) showMainMenu(ctx context.Context, ref *msgRef) error {
	o, err := b.tasks.Overview(ctx, domain.ScopeAll, "")
	if err != nil {
		return b.render(ctx, ref, "Ошибка: "+esc(humanError(err)), kb(row(cb("Повторить", "m"))))
	}
	st := b.settings.Get()
	webAppURL := st.WebAppPublicURL
	var sb strings.Builder
	sb.WriteString("<b>Персональный ассистент</b>\n\n")
	fmt.Fprintf(&sb, "Новые <b>%d</b> · В работе <b>%d</b> · Отложено <b>%d</b> · Просрочено <b>%d</b>\n\n", o.New, o.InProgress, o.Snoozed, o.Overdue)
	switch c := o.Connection; {
	case c == nil:
		sb.WriteString("Telegram Business: <b>не подключён</b>\n<i>Telegram → Настройки → Telegram Business → Чат-боты → выберите этого бота</i>\n")
	case !c.Enabled:
		sb.WriteString("Telegram Business: <b>отключён</b>\n")
	default:
		fmt.Fprintf(&sb, "Telegram Business: подключён (%s), ответы: %s\n", esc(c.UserName), yesNo(c.CanReply))
	}
	if k, ok := st.Primary(); ok {
		fmt.Fprintf(&sb, "AI: <b>%s</b> · <code>%s</code>", providerTitle(k.Provider), esc(st.ModelFor(k.Provider)))
		if n := len(st.AIChain) - 1; n > 0 {
			fmt.Fprintf(&sb, " · резервных ключей: %d", n)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("AI: <b>нет API-ключей</b>\n")
	}
	if st.TriagePaused {
		sb.WriteString("Личный триаж: <b>на паузе</b>")
	} else {
		sb.WriteString("Личный триаж: активен")
	}
	fmt.Fprintf(&sb, " · чувствительность %s · дебаунс %d с\n", sensitivityName(st.Sensitivity), st.DebounceSeconds)
	hd := b.helpdesk.Config()
	switch {
	case hd.Active():
		sb.WriteString("Хелпдеск: включён")
	case hd.Enabled:
		sb.WriteString("Хелпдеск: <b>не указана группа</b>")
	default:
		sb.WriteString("Хелпдеск: выключен")
	}

	rows := [][]button{
		row(cb(fmt.Sprintf("Активные · %d", o.New+o.InProgress), "tl:act:all:0"), cb("Срочные", "tl:act:urg:0")),
	}
	if webAppURL != "" {
		rows = append(rows, row(webAppButton("Открыть веб-панель", webAppURL)))
	}
	rows = append(rows, row(cb("Дайджест", "dg"), cb("Статистика", "sx"), cb("Настройки", "st")))
	return b.render(ctx, ref, sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ---------- settings ----------

// showSettings is the everyday screen: the switches that get changed. Rarely used settings
// (models, digest time, key test, reset) are one tap away on the "Ещё настройки" screen.
func (b *Bot) showSettings(ctx context.Context, ref *msgRef) error {
	st := b.settings.Get()
	var sb strings.Builder
	sb.WriteString("<b>Настройки</b>\n\n")
	if b.cfg.BotDBID != 0 {
		sb.WriteString("Это дополнительный бот. Ниже — общие настройки ИИ; свою группу, приветствие и часы работы " +
			"настройте в веб-панели, раздел «Боты».\n\n")
	}
	sb.WriteString("<b>AI по очереди</b> — если ключ не отвечает, берётся следующий:\n")
	if len(st.AIChain) == 0 {
		sb.WriteString("   ключей нет — триаж не работает\n")
	}
	for i, k := range st.AIChain {
		fmt.Fprintf(&sb, "   %d. %s · <code>%s</code> · <code>%s</code>\n",
			i+1, providerTitle(k.Provider), esc(domain.MaskKey(k.Key)), esc(st.ModelFor(k.Provider)))
	}
	sb.WriteString("   <i>Ключи и порядок меняются в веб-панели</i>\n\n")
	fmt.Fprintf(&sb, "Дебаунс (склейка сообщений): <b>%d с</b>\n", st.DebounceSeconds)
	fmt.Fprintf(&sb, "Чувствительность: <b>%s</b> (порог уверенности %.2f)\n", sensitivityName(st.Sensitivity), st.Sensitivity.Threshold())
	digest := "выкл"
	if st.DigestEnabled {
		digest = "вкл, в " + st.DigestTime
	}
	fmt.Fprintf(&sb, "Утренний дайджест: <b>%s</b> (%s)\n", digest, esc(b.settings.Location().String()))
	if st.TriagePaused {
		sb.WriteString("Личный триаж: <b>на паузе</b> — сообщения Business сохраняются, но не анализируются (хелпдеск не затрагивается)")
	} else {
		sb.WriteString("Личный триаж: <b>активен</b>")
	}

	var debRow []button
	for _, s := range []int{10, 20, 30, 60} {
		debRow = append(debRow, cb(mark(st.DebounceSeconds == s, fmt.Sprintf("%dс", s)), fmt.Sprintf("sd:%d", s)))
	}
	debRow = append(debRow, cb("Свой", "sd:c"))
	sensBtn := func(s domain.Sensitivity, label string) button {
		return cb(mark(st.Sensitivity == s, label), "ss:"+string(s))
	}
	digestToggle := "Дайджест: выкл"
	if st.DigestEnabled {
		digestToggle = "Дайджест: вкл"
	}
	pauseLabel := "Пауза триажа"
	if st.TriagePaused {
		pauseLabel = "Возобновить триаж"
	}
	rows := [][]button{
		debRow,
		row(sensBtn(domain.SensitivityLow, "Низкая"), sensBtn(domain.SensitivityMedium, "Средняя"), sensBtn(domain.SensitivityHigh, "Высокая")),
		row(cb(digestToggle, "sg:t"), cb(pauseLabel, "stp")),
		row(cb("Ещё настройки", "sa")),
	}
	if st.WebAppPublicURL != "" {
		rows = append(rows, row(webAppButton("Все настройки в веб-панели", st.WebAppPublicURL)))
	}
	rows = append(rows, row(cb("Меню", "m")))
	return b.render(ctx, ref, sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) showSettingsMore(ctx context.Context, ref *msgRef) error {
	st := b.settings.Get()
	var sb strings.Builder
	sb.WriteString("<b>Ещё настройки</b>\n\n")
	fmt.Fprintf(&sb, "Отмечать прочитанным при «Взять в работу»: <b>%s</b>\n", yesNo(st.MarkReadOnWork))
	fmt.Fprintf(&sb, "Спрашивать про «Готово» при закрытии: <b>%s</b>\n", yesNo(st.NotifyDoneOnClose))
	fmt.Fprintf(&sb, "Время дайджеста: <b>%s</b>\n\n", esc(st.DigestTime))
	sb.WriteString("Модель — выберите провайдера:")

	var rows [][]button
	var modelRow []button
	for _, p := range st.ChainProviders() {
		modelRow = append(modelRow, cb("Модель: "+providerTitle(p), "sm:"+p))
		if len(modelRow) == 2 {
			rows = append(rows, modelRow)
			modelRow = nil
		}
	}
	if len(modelRow) > 0 {
		rows = append(rows, modelRow)
	}
	rows = append(rows,
		row(cb("Прочитано: "+yesNo(st.MarkReadOnWork), "smr"), cb("«Готово»: "+yesNo(st.NotifyDoneOnClose), "sfd")),
		row(cb("Автозакрытие по «готово»: "+yesNo(st.AutoCloseOnDone), "sac")),
		row(cb("Время дайджеста", "sg:c"), cb("Проверить ключи AI", "stest")),
		row(cb("Сброс к .env", "sreset")),
		row(cb("‹ Назад", "st"), cb("Меню", "m")),
	)
	return b.render(ctx, ref, sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) presets(provider string) []string {
	st := b.settings.Get()
	return st.Provider(provider).Presets
}

func (b *Bot) showModelMenu(ctx context.Context, ref *msgRef, provider string) error {
	if !domainProvider(provider) {
		return domain.ErrInvalidInput
	}
	current := b.settings.Get().ModelFor(provider)
	text := fmt.Sprintf("<b>Модель %s</b>\nТекущая: <code>%s</code>\n\nВыберите пресет или введите идентификатор модели вручную.",
		providerTitle(provider), esc(current))
	var rows [][]button
	for i, m := range b.presets(provider) {
		rows = append(rows, row(cb(mark(m == current, m), fmt.Sprintf("smp:%s:%d", provider, i))))
	}
	rows = append(rows, row(cb("Ввести вручную", "smc:"+provider)), row(cb("‹ Назад", "sa")))
	return b.render(ctx, ref, text, &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ---------- stats ----------

func (b *Bot) showStats(ctx context.Context, ref *msgRef) error {
	const days = 30
	s, err := b.tasks.Stats(ctx, domain.ScopeAll, "", days)
	if err != nil {
		return b.renderError(ctx, ref, err)
	}
	a := s.Analyses
	var created int
	for _, n := range s.Tasks {
		created += n
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>Статистика за %d дней</b>\n\n", days)
	fmt.Fprintf(&sb, "Анализов LLM: <b>%d</b> (ошибок: %d)\n", a.Total, a.Errors)
	fmt.Fprintf(&sb, "Отфильтровано как шум: <b>%d</b>\n", a.Noise)
	fmt.Fprintf(&sb, "Анализов с задачей (создано или дополнено): <b>%d</b>\n", a.WithTask)
	fmt.Fprintf(&sb, "Средняя задержка LLM: <b>%.1f с</b>\n", a.AvgLatencyMs/1000)
	if len(a.ByProvider) > 0 {
		names := make([]string, 0, len(a.ByProvider))
		for p := range a.ByProvider {
			names = append(names, p)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, p := range names {
			parts = append(parts, fmt.Sprintf("%s %d", providerTitle(p), a.ByProvider[p]))
		}
		fmt.Fprintf(&sb, "По провайдерам: %s\n", strings.Join(parts, " · "))
	}
	fmt.Fprintf(&sb, "\nЗадачи: новые %d · в работе %d · отложено %d · завершено %d · не задачи %d\n",
		s.Tasks[domain.StatusNew], s.Tasks[domain.StatusInProgress], s.Tasks[domain.StatusSnoozed],
		s.Tasks[domain.StatusDone], s.Tasks[domain.StatusFalsePositive])
	if created > 0 {
		fp := s.Tasks[domain.StatusFalsePositive]
		fmt.Fprintf(&sb, "Точность триажа: <b>%.0f%%</b> (ложных срабатываний %d из %d)",
			100*(1-float64(fp)/float64(created)), fp, created)
	}
	return b.render(ctx, ref, sb.String(), kb(row(cb("Не задачи", "tl:fp:all:0")), row(cb("Обновить", "sx"), cb("Меню", "m"))))
}

// ---------- digest ----------

func (b *Bot) sendDigest(ctx context.Context) error {
	d, err := b.tasks.BuildDigest(ctx, domain.ScopeAll, "")
	if err != nil {
		return err
	}
	text, markup := b.digestView(d)
	return b.sendText(ctx, text, markup)
}

func (b *Bot) digestView(d *service.Digest) (string, *telegram.InlineKeyboardMarkup) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>Дайджест задач</b> · %s\n", d.Date.Format("02.01.2006"))
	if d.Empty() {
		sb.WriteString("\nОткрытых задач нет.")
		return sb.String(), kb(row(cb("Меню", "m")))
	}
	fmt.Fprintf(&sb, "Открыто: <b>%d</b> (новые %d · в работе %d · отложено %d)\n", d.New+d.InProgress+d.Snoozed, d.New, d.InProgress, d.Snoozed)

	const maxItems = 10
	section := func(title string, items []domain.Task, detail func(t domain.Task) string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n%s (%d):\n", title, len(items))
		for i, t := range items {
			if i == maxItems {
				fmt.Fprintf(&sb, "…и ещё %d\n", len(items)-maxItems)
				break
			}
			fmt.Fprintf(&sb, "%s <b>#%d</b> %s — %s\n", priorityBars(t.Priority), t.ID, esc(trunc(t.Title, 60)), detail(t))
		}
	}
	byDeadline := func(t domain.Task) string {
		return esc(trunc(t.SenderName, 30)) + ", до " + b.fmtShort(*t.Deadline)
	}
	section("<b>Просрочено</b>", d.Overdue, byDeadline)
	section("<b>Дедлайн в ближайшие 24 ч</b>", d.DueSoon, byDeadline)
	section("<b>Висят без реакции больше суток</b>", d.Stale, func(t domain.Task) string {
		return esc(trunc(t.SenderName, 30)) + ", с " + b.fmtShort(t.CreatedAt)
	})
	if len(d.Overdue)+len(d.DueSoon)+len(d.Stale) == 0 {
		sb.WriteString("\nПросроченных и зависших задач нет.")
	}

	var rows [][]button
	for _, group := range [][]domain.Task{d.Overdue, d.DueSoon, d.Stale} {
		for _, t := range group {
			if len(rows) == 6 {
				break
			}
			rows = append(rows, row(cb(fmt.Sprintf("#%d %s", t.ID, trunc(t.Title, 44)), fmt.Sprintf("tv:%d", t.ID))))
		}
	}
	rows = append(rows, row(cb("Все активные", "tl:act:all:0"), cb("Меню", "m")))
	return sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}
