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
	{"fp", "Ошибки", []domain.TaskStatus{domain.StatusFalsePositive}},
	{"all", "Все", nil},
}

type prioFilter struct {
	key, short, title string
	prios             []domain.Priority
}

var prioFilters = []prioFilter{
	{"all", "Все", "все приоритеты", nil},
	{"urg", "🔥", "срочные", []domain.Priority{domain.PriorityCritical, domain.PriorityHigh}},
	{"crit", "🔴", "критические", []domain.Priority{domain.PriorityCritical}},
	{"high", "🟠", "высокие", []domain.Priority{domain.PriorityHigh}},
	{"med", "🟡", "средние", []domain.Priority{domain.PriorityMedium}},
	{"low", "🟢", "низкие", []domain.Priority{domain.PriorityLow}},
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
	fmt.Fprintf(&sb, "📋 <b>Задачи</b> · %s · %s\n", sf.label, pf.title)
	fmt.Fprintf(&sb, "Найдено: <b>%d</b> · стр. %d/%d\n", total, q.Page+1, pages)
	if total == 0 {
		sb.WriteString("\n<i>Здесь пусто</i> 🎉")
	}

	now := time.Now()
	var rows [][]button
	for _, t := range items {
		fmt.Fprintf(&sb, "\n%s <b>#%d</b> %s\n", priorityEmoji(t.Priority), t.ID, esc(trunc(t.Title, 80)))
		meta := []string{"👤 " + esc(trunc(t.SenderName, 30)), categoryLabel(t.Category)}
		if len(sf.statuses) != 1 {
			meta = append(meta, statusLabel(t.Status))
		}
		if t.Deadline != nil {
			d := "📅 " + b.fmtShort(*t.Deadline)
			if t.IsOverdue(now) {
				d += " ⚠️"
			}
			meta = append(meta, d)
		}
		if t.Status == domain.StatusSnoozed && t.SnoozeUntil != nil {
			meta = append(meta, "⏰ до "+b.fmtShort(*t.SnoozeUntil))
		}
		fmt.Fprintf(&sb, "      %s\n", strings.Join(meta, " · "))
		rows = append(rows, row(cb(fmt.Sprintf("%s #%d %s", priorityEmoji(t.Priority), t.ID, trunc(t.Title, 40)), fmt.Sprintf("tv:%d", t.ID))))
	}

	if pages > 1 {
		var nav []button
		if q.Page > 0 {
			nav = append(nav, cb("◀️", listQuery{q.Status, q.Prio, q.Page - 1}.data()))
		}
		nav = append(nav, cb(fmt.Sprintf("%d / %d", q.Page+1, pages), "noop"))
		if q.Page+1 < pages {
			nav = append(nav, cb("▶️", listQuery{q.Status, q.Prio, q.Page + 1}.data()))
		}
		rows = append(rows, nav)
	}
	var fr []button
	for i, s := range statusFilters {
		fr = append(fr, cb(mark(s.key == q.Status, s.label), listQuery{s.key, q.Prio, 0}.data()))
		if len(fr) == 4 || i == len(statusFilters)-1 {
			rows = append(rows, fr)
			fr = nil
		}
	}
	var pr []button
	for _, p := range prioFilters {
		pr = append(pr, cb(mark(p.key == q.Prio, p.short), listQuery{q.Status, p.key, 0}.data()))
	}
	rows = append(rows, pr, row(cb("🔄 Обновить", q.data()), cb("🏠 Меню", "m")))
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
	err := b.render(ctx, ref, b.taskCardText(t, header, b.cfg.DeepLinks), markup)
	if err != nil && b.cfg.DeepLinks && telegram.IsEntityError(err) {
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
	fmt.Fprintf(&sb, "%s <b>#%d · %s</b>\n", priorityEmoji(t.Priority), t.ID, esc(trunc(t.Title, 150)))
	status := statusLabel(t.Status)
	if t.Status == domain.StatusSnoozed && t.SnoozeUntil != nil {
		status += " до " + b.fmtShort(*t.SnoozeUntil)
	}
	fmt.Fprintf(&sb, "Статус: %s\n\n", status)

	sender := esc(trunc(t.SenderName, 60))
	if links {
		sender = fmt.Sprintf(`<a href="%s">%s</a>`, esc(profileURL(t.SenderUsername, t.SenderID)), sender)
	}
	if t.SenderUsername != "" {
		sender += " (@" + esc(t.SenderUsername) + ")"
	}
	fmt.Fprintf(&sb, "👤 От: %s\n", sender)
	if links && t.FirstSourceMessageID() > 0 {
		fmt.Fprintf(&sb, "💬 <a href=\"%s\">Открыть исходное сообщение</a>\n", esc(messageURL(t.ChatID, t.FirstSourceMessageID())))
	}
	fmt.Fprintf(&sb, "🏷 %s · ⚡ приоритет %s\n", categoryLabel(t.Category), priorityName(t.Priority))
	if t.Deadline != nil {
		d := b.fmtTime(*t.Deadline)
		if t.IsOverdue(now) {
			d += " ⚠️ <b>просрочено</b>"
		}
		fmt.Fprintf(&sb, "📅 Дедлайн: %s\n", d)
	}
	fmt.Fprintf(&sb, "🕒 Получено: %s\n", b.fmtTime(t.CreatedAt))

	if t.Description != "" {
		fmt.Fprintf(&sb, "\n📝 <b>Суть:</b>\n%s\n", esc(trunc(t.Description, 1200)))
	}
	if t.SourceText != "" {
		fmt.Fprintf(&sb, "\n💬 <b>Исходный текст:</b>\n<blockquote expandable>%s</blockquote>\n", esc(trunc(t.SourceText, 1500)))
	}
	if t.DraftReply != "" && t.ReplySentAt == nil {
		fmt.Fprintf(&sb, "\n✍️ <b>Черновик ответа</b> (%s):\n<i>%s</i>\n", strategyLabel(t.ReplyStrategy), esc(trunc(t.DraftReply, 800)))
	}
	if t.ReplySentAt != nil {
		fmt.Fprintf(&sb, "\n📤 <b>Ответ отправлен</b> %s:\n<i>%s</i>\n", b.fmtShort(*t.ReplySentAt), esc(trunc(t.ReplyText, 600)))
	}
	if t.Provider != "" {
		fmt.Fprintf(&sb, "\n<i>🤖 %s · %s · уверенность %.0f%%</i>", providerTitle(t.Provider), esc(t.Model), t.Confidence*100)
	}
	return sb.String()
}

func (b *Bot) taskKeyboard(t *domain.Task) *telegram.InlineKeyboardMarkup {
	id := t.ID
	data := func(action string) string { return fmt.Sprintf("ta:%s:%d", action, id) }
	var rows [][]button
	if t.Status.IsOpen() {
		var r1 []button
		if t.DraftReply != "" {
			label := "🚀 Ответить черновиком"
			if t.ReplySentAt != nil {
				label = "🚀 Отправить черновик ещё раз"
			}
			r1 = append(r1, cb(label, data("draft")))
		}
		r1 = append(r1, cb("✏️ Свой ответ", data("reply")))
		rows = append(rows, r1)

		var r2 []button
		if t.Status != domain.StatusInProgress {
			r2 = append(r2, cb("👀 В работу", data("work")))
		}
		r2 = append(r2, cb("✅ Закрыть", data("done")))
		rows = append(rows, r2, row(cb("⏰ Отложить", data("snz")), cb("🗑 Ошибка", data("fp"))))
	} else {
		rows = append(rows, row(cb("♻️ Вернуть в работу", data("reopen")), cb("✏️ Написать", data("reply"))))
	}
	rows = append(rows, row(cb("⬅️ К списку", b.states.list().data()), cb("🏠 Меню", "m")))
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func snoozeKeyboard(id int64) *telegram.InlineKeyboardMarkup {
	d := func(v string) string { return fmt.Sprintf("ts:%d:%s", id, v) }
	return kb(
		row(cb("30 мин", d("30")), cb("1 час", d("60")), cb("3 часа", d("180"))),
		row(cb("Завтра 09:00", d("tm")), cb("3 дня", d("4320")), cb("Неделя", d("10080"))),
		row(cb("✍️ Свой срок", d("c"))),
		row(cb("⬅️ Назад", fmt.Sprintf("tv:%d", id))),
	)
}

func closeConfirmKeyboard(id int64) *telegram.InlineKeyboardMarkup {
	return kb(
		row(cb("✅ Закрыть + отправить «Готово!»", fmt.Sprintf("tc:%d:y", id))),
		row(cb("Просто закрыть", fmt.Sprintf("tc:%d:n", id))),
		row(cb("⬅️ Назад", fmt.Sprintf("tv:%d", id))),
	)
}

// ---------- main menu ----------

func (b *Bot) showMainMenu(ctx context.Context, ref *msgRef) error {
	o, err := b.tasks.Overview(ctx)
	if err != nil {
		return b.render(ctx, ref, "❌ "+esc(humanError(err)), kb(row(cb("🔄 Повторить", "m"))))
	}
	st := b.settings.Get()
	var sb strings.Builder
	sb.WriteString("🤖 <b>Персональный ассистент</b>\n\n")
	switch c := o.Connection; {
	case c == nil:
		sb.WriteString("🔗 Telegram Business: ❌ не подключён\n<i>Telegram → Настройки → Telegram Business → Чат-боты → выберите этого бота</i>\n")
	case !c.Enabled:
		sb.WriteString("🔗 Telegram Business: ⏸ отключён\n")
	default:
		fmt.Fprintf(&sb, "🔗 Telegram Business: ✅ %s · ответы: %s\n", esc(c.UserName), yesNo(c.CanReply))
	}
	fmt.Fprintf(&sb, "🧠 AI: <b>%s</b> · <code>%s</code>\n", providerTitle(st.ActiveProvider), esc(st.ActiveModel()))
	if st.TriagePaused {
		sb.WriteString("⏸ Триаж: <b>на паузе</b>")
	} else {
		sb.WriteString("▶️ Триаж: активен")
	}
	fmt.Fprintf(&sb, " · дебаунс %d с · чувствительность %s\n\n", st.DebounceSeconds, sensitivityName(st.Sensitivity))
	fmt.Fprintf(&sb, "🆕 Новые: <b>%d</b> · 👀 В работе: <b>%d</b>\n⏰ Отложено: <b>%d</b> · ⚠️ Просрочено: <b>%d</b>",
		o.New, o.InProgress, o.Snoozed, o.Overdue)

	rows := [][]button{
		row(cb("📋 Активные", "tl:act:all:0"), cb("🔥 Срочные", "tl:act:urg:0")),
		row(cb("🆕 Новые", "tl:new:all:0"), cb("⏰ Отложенные", "tl:snz:all:0")),
		row(cb("✅ Завершённые", "tl:done:all:0"), cb("🗑 Ошибки", "tl:fp:all:0")),
		row(cb("🌅 Дайджест", "dg"), cb("📊 Статистика", "sx")),
	}
	if b.cfg.WebAppURL != "" {
		rows = append(rows, row(webAppButton("📱 Открыть веб-панель", b.cfg.WebAppURL)))
	}
	rows = append(rows, row(cb("⚙️ Настройки", "st"), cb("🔄 Обновить", "m")))
	return b.render(ctx, ref, sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ---------- settings ----------

func (b *Bot) showSettings(ctx context.Context, ref *msgRef) error {
	st := b.settings.Get()
	keyNote := func(p string) string {
		if b.settings.HasProvider(p) {
			return ""
		}
		return " <i>(нет API-ключа)</i>"
	}
	var sb strings.Builder
	sb.WriteString("⚙️ <b>Настройки</b>\n\n")
	fmt.Fprintf(&sb, "🤖 Активный провайдер: <b>%s</b>\n", providerTitle(st.ActiveProvider))
	fmt.Fprintf(&sb, "   • Claude: <code>%s</code>%s\n", esc(st.ClaudeModel), keyNote(domain.ProviderClaude))
	fmt.Fprintf(&sb, "   • Gemini: <code>%s</code>%s\n", esc(st.GeminiModel), keyNote(domain.ProviderGemini))
	fmt.Fprintf(&sb, "   • Groq: <code>%s</code>%s\n", esc(st.GroqModel), keyNote(domain.ProviderGroq))
	fmt.Fprintf(&sb, "   • Mistral: <code>%s</code>%s\n", esc(st.MistralModel), keyNote(domain.ProviderMistral))
	fmt.Fprintf(&sb, "   • OpenRouter: <code>%s</code>%s\n", esc(st.OpenRouterModel), keyNote(domain.ProviderOpenRouter))
	fmt.Fprintf(&sb, "⏱ Дебаунс (склейка сообщений): <b>%d с</b>\n", st.DebounceSeconds)
	fmt.Fprintf(&sb, "🎯 Чувствительность: <b>%s</b> (порог уверенности %.2f)\n", sensitivityName(st.Sensitivity), st.Sensitivity.Threshold())
	digest := "выкл"
	if st.DigestEnabled {
		digest = "вкл, в " + st.DigestTime
	}
	fmt.Fprintf(&sb, "🌅 Утренний дайджест: <b>%s</b> (%s)\n", digest, esc(b.cfg.Location.String()))
	if st.TriagePaused {
		sb.WriteString("⏸ Триаж: <b>на паузе</b> — новые сообщения сохраняются, но не анализируются\n")
	} else {
		sb.WriteString("▶️ Триаж: <b>активен</b>\n")
	}
	fmt.Fprintf(&sb, "👁 Отмечать прочитанным при «В работу»: <b>%s</b>\n", yesNo(st.MarkReadOnWork))
	fmt.Fprintf(&sb, "💬 Спрашивать про «Готово!» при закрытии: <b>%s</b>", yesNo(st.NotifyDoneOnClose))

	provBtn := func(p string) button {
		return cb(mark(st.ActiveProvider == p, "🤖 "+providerTitle(p)), "sp:"+p)
	}
	var debRow []button
	for _, s := range []int{10, 20, 30, 60} {
		debRow = append(debRow, cb(mark(st.DebounceSeconds == s, fmt.Sprintf("%dс", s)), fmt.Sprintf("sd:%d", s)))
	}
	debRow = append(debRow, cb("✍️", "sd:c"))
	sensBtn := func(s domain.Sensitivity, label string) button {
		return cb(mark(st.Sensitivity == s, label), "ss:"+string(s))
	}
	digestToggle := "🌅 Дайджест: выкл"
	if st.DigestEnabled {
		digestToggle = "🌅 Дайджест: вкл"
	}
	pauseLabel := "⏸ Пауза триажа"
	if st.TriagePaused {
		pauseLabel = "▶️ Возобновить триаж"
	}
	return b.render(ctx, ref, sb.String(), kb(
		row(provBtn(domain.ProviderClaude), provBtn(domain.ProviderGemini), provBtn(domain.ProviderGroq)),
		row(provBtn(domain.ProviderMistral), provBtn(domain.ProviderOpenRouter)),
		row(cb("✏️ Claude", "sm:claude"), cb("✏️ Gemini", "sm:gemini"), cb("✏️ Groq", "sm:groq")),
		row(cb("✏️ Mistral", "sm:mistral"), cb("✏️ OpenRouter", "sm:openrouter")),
		debRow,
		row(sensBtn(domain.SensitivityLow, "🎯 Низкая"), sensBtn(domain.SensitivityMedium, "Средняя"), sensBtn(domain.SensitivityHigh, "Высокая")),
		row(cb(digestToggle, "sg:t"), cb("🕘 Время дайджеста", "sg:c")),
		row(cb(pauseLabel, "stp"), cb("👁 Прочитано: "+yesNo(st.MarkReadOnWork), "smr")),
		row(cb("💬 «Готово!» при закрытии: "+yesNo(st.NotifyDoneOnClose), "sfd")),
		row(cb("🧪 Проверить провайдера", "stest")),
		row(cb("♻️ Сброс к .env", "sreset"), cb("🏠 Меню", "m")),
	))
}

func (b *Bot) presets(provider string) []string {
	switch provider {
	case domain.ProviderGemini:
		return b.cfg.GeminiPresets
	case domain.ProviderGroq:
		return b.cfg.GroqPresets
	case domain.ProviderMistral:
		return b.cfg.MistralPresets
	case domain.ProviderOpenRouter:
		return b.cfg.OpenRouterPresets
	default:
		return b.cfg.ClaudePresets
	}
}

func (b *Bot) showModelMenu(ctx context.Context, ref *msgRef, provider string) error {
	if !domainProvider(provider) {
		return domain.ErrInvalidInput
	}
	current := b.settings.Get().ModelFor(provider)
	text := fmt.Sprintf("🧠 <b>Модель %s</b>\nТекущая: <code>%s</code>\n\nВыберите пресет или введите идентификатор модели вручную.",
		providerTitle(provider), esc(current))
	var rows [][]button
	for i, m := range b.presets(provider) {
		rows = append(rows, row(cb(mark(m == current, m), fmt.Sprintf("smp:%s:%d", provider, i))))
	}
	rows = append(rows, row(cb("✍️ Ввести вручную", "smc:"+provider)), row(cb("⬅️ Назад", "st")))
	return b.render(ctx, ref, text, &telegram.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ---------- stats ----------

func (b *Bot) showStats(ctx context.Context, ref *msgRef) error {
	const days = 30
	s, err := b.tasks.Stats(ctx, days)
	if err != nil {
		return b.renderError(ctx, ref, err)
	}
	a := s.Analyses
	var created int
	for _, n := range s.Tasks {
		created += n
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 <b>Статистика за %d дней</b>\n\n", days)
	fmt.Fprintf(&sb, "🧠 Анализов LLM: <b>%d</b> (ошибок: %d)\n", a.Total, a.Errors)
	fmt.Fprintf(&sb, "🔇 Отфильтровано как шум: <b>%d</b>\n", a.Noise)
	fmt.Fprintf(&sb, "📌 Анализов с задачей (создано/дополнено): <b>%d</b>\n", a.WithTask)
	fmt.Fprintf(&sb, "⏱ Средняя задержка LLM: <b>%.1f с</b>\n", a.AvgLatencyMs/1000)
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
		fmt.Fprintf(&sb, "🤖 По провайдерам: %s\n", strings.Join(parts, " · "))
	}
	fmt.Fprintf(&sb, "\n📋 Задачи: 🆕 %d · 👀 %d · ⏰ %d · ✅ %d · 🗑 %d\n",
		s.Tasks[domain.StatusNew], s.Tasks[domain.StatusInProgress], s.Tasks[domain.StatusSnoozed],
		s.Tasks[domain.StatusDone], s.Tasks[domain.StatusFalsePositive])
	if created > 0 {
		fp := s.Tasks[domain.StatusFalsePositive]
		fmt.Fprintf(&sb, "🎯 Точность триажа: <b>%.0f%%</b> (ложных срабатываний %d из %d)",
			100*(1-float64(fp)/float64(created)), fp, created)
	}
	return b.render(ctx, ref, sb.String(), kb(row(cb("🗑 Ложные срабатывания", "tl:fp:all:0")), row(cb("🔄 Обновить", "sx"), cb("🏠 Меню", "m"))))
}

// ---------- digest ----------

func (b *Bot) sendDigest(ctx context.Context) error {
	d, err := b.tasks.BuildDigest(ctx)
	if err != nil {
		return err
	}
	text, markup := b.digestView(d)
	return b.sendText(ctx, text, markup)
}

func (b *Bot) digestView(d *service.Digest) (string, *telegram.InlineKeyboardMarkup) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "🌅 <b>Дайджест задач</b> · %s\n", d.Date.Format("02.01.2006"))
	if d.Empty() {
		sb.WriteString("\nОткрытых задач нет 🎉")
		return sb.String(), kb(row(cb("🏠 Меню", "m")))
	}
	fmt.Fprintf(&sb, "Открыто: <b>%d</b> (🆕 %d · 👀 %d · ⏰ %d)\n", d.New+d.InProgress+d.Snoozed, d.New, d.InProgress, d.Snoozed)

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
			fmt.Fprintf(&sb, "• %s <b>#%d</b> %s — %s\n", priorityEmoji(t.Priority), t.ID, esc(trunc(t.Title, 60)), detail(t))
		}
	}
	byDeadline := func(t domain.Task) string {
		return esc(trunc(t.SenderName, 30)) + ", до " + b.fmtShort(*t.Deadline)
	}
	section("🔥 <b>Просрочено</b>", d.Overdue, byDeadline)
	section("⏳ <b>Дедлайн в ближайшие 24 ч</b>", d.DueSoon, byDeadline)
	section("🕸 <b>Висят без реакции больше суток</b>", d.Stale, func(t domain.Task) string {
		return esc(trunc(t.SenderName, 30)) + ", с " + b.fmtShort(t.CreatedAt)
	})
	if len(d.Overdue)+len(d.DueSoon)+len(d.Stale) == 0 {
		sb.WriteString("\nПросроченных и зависших задач нет ✅")
	}

	var rows [][]button
	for _, group := range [][]domain.Task{d.Overdue, d.DueSoon, d.Stale} {
		for _, t := range group {
			if len(rows) == 6 {
				break
			}
			rows = append(rows, row(cb(fmt.Sprintf("%s #%d %s", priorityEmoji(t.Priority), t.ID, trunc(t.Title, 40)), fmt.Sprintf("tv:%d", t.ID))))
		}
	}
	rows = append(rows, row(cb("📋 Все активные", "tl:act:all:0"), cb("🏠 Меню", "m")))
	return sb.String(), &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}
