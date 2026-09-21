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

// The tickets topic of the helpdesk group holds no ticket cards (those live in the user's topic):
// only a pinned management menu and the morning digest of open tickets.
//
// Callback data of the menu (<= 64 bytes):
//   hm:r                                 menu
//   hm:l:<filter>:<page>                 list of tickets
//   hm:t:<id>:<filter>:<page>            one ticket with its actions
//   hm:a:<action>:<id>:<filter>:<page>   a ticket action, see ticketAction ("snz" opens the snooze periods)
// Filters: n new, w in progress, s snoozed, o overdue, a all open.

const ticketsPageSize = 8

// ---------- reminders ----------

// reminderKeyboard is the keyboard of the "user is waiting" reminder: hr:m:<user id> opens the
// snooze periods, hr:<option>:<user id> snoozes, hr:b:<user id> goes back.
func reminderKeyboard(userID int64) *telegram.InlineKeyboardMarkup {
	return kb(row(cb("Отложить…", fmt.Sprintf("hr:m:%d", userID))))
}

func (b *Bot) reminderCallback(ctx context.Context, ref *msgRef, p []string, answer func(string, bool)) error {
	userID, err := strconv.ParseInt(p[2], 10, 64)
	if err != nil {
		return domain.ErrInvalidInput
	}
	setMarkup := func(m *telegram.InlineKeyboardMarkup) error {
		if ref == nil {
			return nil
		}
		if err := b.api.EditMessageReplyMarkup(ctx, ref.ChatID, ref.MessageID, m); err != nil && !telegram.IsNotModified(err) {
			return err
		}
		return nil
	}
	switch p[1] {
	case "m":
		return setMarkup(snoozePeriods(func(opt string) string { return fmt.Sprintf("hr:%s:%d", opt, userID) }, fmt.Sprintf("hr:b:%d", userID)))
	case "b":
		return setMarkup(reminderKeyboard(userID))
	}
	until, err := b.snoozeDeadline(p[1])
	if err != nil {
		return err
	}
	if err := b.helpdesk.SnoozeReminder(ctx, userID, until); err != nil {
		return err
	}
	answer("Напомню после "+b.fmtShort(until), false)
	if ref == nil {
		return nil
	}
	text := fmt.Sprintf("😴 <b>Напоминание отложено до %s</b>", b.fmtShort(until))
	if by := service.ActorFrom(ctx).Name; by != "" {
		text += " · " + esc(trunc(by, 40))
	}
	err = b.api.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: ref.ChatID, MessageID: ref.MessageID, Text: text,
		ParseMode: "HTML", ReplyMarkup: reminderKeyboard(userID)})
	if err != nil && !telegram.IsNotModified(err) {
		b.log.Debug("edit reminder", "err", err)
	}
	return nil
}

// ---------- tickets menu ----------

// ticketFilter describes one list of the menu.
type ticketFilter struct {
	title    string
	statuses []domain.TaskStatus
	overdue  bool
}

var ticketFilters = map[string]ticketFilter{
	"n": {title: "Новые", statuses: []domain.TaskStatus{domain.StatusNew}},
	"w": {title: "В работе", statuses: []domain.TaskStatus{domain.StatusInProgress}},
	"s": {title: "Отложенные", statuses: []domain.TaskStatus{domain.StatusSnoozed}},
	"o": {title: "Просроченные", statuses: []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress}, overdue: true},
	"a": {title: "Все открытые", statuses: []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress, domain.StatusSnoozed}},
}

// botConn is the connection_id of this bot's tickets.
func (b *Bot) botConn() string { return domain.HelpdeskConnectionFor(b.cfg.BotDBID) }

// ownTicket loads a ticket of this bot's desk.
func (b *Bot) ownTicket(ctx context.Context, id int64) (*domain.Task, error) {
	t, err := b.tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.ConnectionID != b.botConn() {
		return nil, domain.ErrForbidden
	}
	return t, nil
}

// ticketList returns the tickets of a filter, most urgent first.
func (b *Bot) ticketList(ctx context.Context, f ticketFilter) ([]domain.Task, error) {
	tasks, _, err := b.tasks.List(ctx, domain.TaskFilter{Statuses: f.statuses, ConnectionID: b.botConn(), Limit: 200})
	if err != nil || !f.overdue {
		return tasks, err
	}
	now := time.Now()
	out := tasks[:0]
	for _, t := range tasks {
		if t.IsOverdue(now) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (b *Bot) ticketsMenuView(ctx context.Context) (string, *telegram.InlineKeyboardMarkup, error) {
	o, err := b.tasks.Overview(ctx, domain.ScopeHelpdesk, b.botConn())
	if err != nil {
		return "", nil, err
	}
	text := fmt.Sprintf("<b>🎫 Тикеты</b>\n\nНовые <b>%d</b> · В работе <b>%d</b> · Отложено <b>%d</b> · Просрочено <b>%d</b>\n\n"+
		"<i>Откройте список и выберите тикет: действия над ним доступны прямо здесь. Карточки тикетов — в темах пользователей. "+
		"Цифры обновляются при нажатии «Обновить».</i>", o.New, o.InProgress, o.Snoozed, o.Overdue)
	rows := [][]button{
		row(cb(fmt.Sprintf("Новые · %d", o.New), "hm:l:n:0"), cb(fmt.Sprintf("В работе · %d", o.InProgress), "hm:l:w:0")),
		row(cb(fmt.Sprintf("Отложенные · %d", o.Snoozed), "hm:l:s:0"), cb(fmt.Sprintf("Просрочено · %d", o.Overdue), "hm:l:o:0")),
		row(cb(fmt.Sprintf("Все открытые · %d", o.New+o.InProgress+o.Snoozed), "hm:l:a:0")),
	}
	last := row(cb("Обновить", "hm:r"))
	if b.settings.Get().WebAppPublicURL != "" && b.cfg.BotUsername != "" {
		last = append(last, button{Text: "Веб-панель", URL: fmt.Sprintf("https://t.me/%s?start=panel", b.cfg.BotUsername)})
	}
	return text, kb(append(rows, last)...), nil
}

func (b *Bot) ticketsListView(ctx context.Context, key string, page int) (string, *telegram.InlineKeyboardMarkup, error) {
	f, ok := ticketFilters[key]
	if !ok {
		return "", nil, domain.ErrInvalidInput
	}
	tasks, err := b.ticketList(ctx, f)
	if err != nil {
		return "", nil, err
	}
	pages := max((len(tasks)+ticketsPageSize-1)/ticketsPageSize, 1)
	page = min(max(page, 0), pages-1)
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s</b> · %d", f.title, len(tasks))
	if len(tasks) == 0 {
		sb.WriteString("\n\nТикетов нет.")
	}
	var rows [][]button
	for _, t := range tasks[min(page*ticketsPageSize, len(tasks)):min((page+1)*ticketsPageSize, len(tasks))] {
		label := fmt.Sprintf("#%d %s — %s", t.ID, trunc(t.SenderName, 16), t.Title)
		rows = append(rows, row(cb(trunc(label, 56), fmt.Sprintf("hm:t:%d:%s:%d", t.ID, key, page))))
	}
	if pages > 1 {
		rows = append(rows, row(cb("‹", fmt.Sprintf("hm:l:%s:%d", key, max(page-1, 0))), cb(fmt.Sprintf("%d / %d", page+1, pages), "noop"),
			cb("›", fmt.Sprintf("hm:l:%s:%d", key, min(page+1, pages-1)))))
	}
	rows = append(rows, row(cb("‹ Меню", "hm:r")))
	return sb.String(), kb(rows...), nil
}

// cardURL links to a ticket's card in the user's topic ("" when there is none).
func (b *Bot) cardURL(ctx context.Context, taskID int64) string {
	cards, err := b.helpdesk.Cards(ctx, taskID)
	if err != nil {
		return ""
	}
	for _, c := range cards {
		if c.ChatID == b.helpdesk.GroupID() {
			return fmt.Sprintf("%s/%d", domain.TopicLink(c.ChatID, c.TopicID), c.MessageID)
		}
	}
	return ""
}

func (b *Bot) ticketsTicketView(ctx context.Context, id int64, key string, page int, snooze bool) (string, *telegram.InlineKeyboardMarkup, error) {
	t, err := b.ownTicket(ctx, id)
	if err != nil {
		return "", nil, err
	}
	text := b.ticketCardText(t, b.ticketUser(ctx, t), false)
	act := func(action string) string { return fmt.Sprintf("hm:a:%s:%d:%s:%d", action, t.ID, key, page) }
	if snooze {
		return text, snoozePeriods(func(opt string) string { return act("z" + opt) }, fmt.Sprintf("hm:t:%d:%s:%d", t.ID, key, page)), nil
	}
	var rows [][]button
	if t.Status.IsOpen() {
		if t.DraftReply != "" && t.ReplySentAt == nil && t.HasChat() {
			rows = append(rows, row(cb("Отправить черновик", act("draft"))))
		}
		r := row(cb("Закрыть", act("done")))
		if t.Status != domain.StatusInProgress {
			r = row(cb("В работу", act("work")), r[0])
		}
		rows = append(rows, r, row(cb("Отложить…", act("snz")), cb("Не задача", act("fp"))))
	} else {
		rows = append(rows, row(cb("Вернуть в работу", act("reopen"))))
	}
	var links []button
	if link := b.cardURL(ctx, t.ID); link != "" {
		links = append(links, button{Text: "Карточка", URL: link})
	}
	if link := b.panelTicketLink(t); link != "" {
		links = append(links, button{Text: "В панели", URL: link})
	}
	if len(links) > 0 {
		rows = append(rows, links)
	}
	rows = append(rows, row(cb("‹ К списку", fmt.Sprintf("hm:l:%s:%d", key, page))))
	return text, kb(rows...), nil
}

// editMenu rewrites the menu message in place.
func (b *Bot) editMenu(ctx context.Context, ref *msgRef, text string, markup *telegram.InlineKeyboardMarkup) error {
	if ref == nil {
		return nil
	}
	err := b.api.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: ref.ChatID, MessageID: ref.MessageID, Text: text,
		ParseMode: "HTML", ReplyMarkup: markup, LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true}})
	if err != nil && !telegram.IsNotModified(err) {
		return err
	}
	return nil
}

func (b *Bot) menuCallback(ctx context.Context, ref *msgRef, p []string, answer func(string, bool)) error {
	num := func(i int) int64 {
		if i >= len(p) {
			return 0
		}
		n, _ := strconv.ParseInt(p[i], 10, 64)
		return n
	}
	str := func(i int) string {
		if i >= len(p) {
			return ""
		}
		return p[i]
	}
	var (
		text   string
		markup *telegram.InlineKeyboardMarkup
		err    error
	)
	switch p[1] {
	case "r":
		text, markup, err = b.ticketsMenuView(ctx)
	case "l":
		text, markup, err = b.ticketsListView(ctx, str(2), int(num(3)))
	case "t":
		text, markup, err = b.ticketsTicketView(ctx, num(2), str(3), int(num(4)), false)
	case "a":
		action, id, key, page := str(2), num(3), str(4), int(num(5))
		if action == "snz" {
			text, markup, err = b.ticketsTicketView(ctx, id, key, page, true)
			break
		}
		var t *domain.Task
		if t, err = b.ownTicket(ctx, id); err != nil {
			break
		}
		var notice string
		if notice, err = b.ticketAction(ctx, t, action); err != nil {
			break
		}
		answer(notice, false)
		text, markup, err = b.ticketsTicketView(ctx, id, key, page, false)
	default:
		return domain.ErrInvalidInput
	}
	if err != nil {
		return err
	}
	return b.editMenu(ctx, ref, text, markup)
}

// sendToTicketsTopic posts into the tickets topic, recreating the topic once if it was deleted.
func (b *Bot) sendToTicketsTopic(ctx context.Context, text string, markup *telegram.InlineKeyboardMarkup) (int64, int, *telegram.Message, error) {
	for attempt := 0; ; attempt++ {
		group, topic, err := b.helpdesk.TicketsTopic(ctx)
		if err != nil {
			return 0, 0, nil, err
		}
		m, err := b.api.SendMessage(ctx, telegram.SendMessageParams{ChatID: group, MessageThreadID: topic, Text: text, ParseMode: "HTML",
			ReplyMarkup: markup, LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true}})
		if err != nil && telegram.IsTopicGone(err) && attempt == 0 {
			b.helpdesk.ForgetTicketsTopic(ctx, group)
			continue
		}
		return group, topic, m, err
	}
}

// EnsureTicketsMenu posts the management menu into the tickets topic when it is not there yet and,
// once, removes the ticket cards older versions duplicated into that topic.
func (b *Bot) EnsureTicketsMenu(ctx context.Context) { b.postTicketsMenu(ctx, false) }

func (b *Bot) postTicketsMenu(ctx context.Context, force bool) {
	if !b.helpdesk.Active() {
		return
	}
	group, topic, err := b.helpdesk.TicketsTopic(ctx)
	if err != nil {
		b.log.Warn("tickets topic", "err", err)
		return
	}
	old := b.helpdesk.TicketsMenuID(ctx, group)
	if old == 0 || force {
		text, markup, err := b.ticketsMenuView(ctx)
		if err != nil {
			b.log.Warn("build tickets menu", "err", err)
			return
		}
		var m *telegram.Message
		if group, topic, m, err = b.sendToTicketsTopic(ctx, text, markup); err != nil {
			b.log.Warn("post tickets menu", "err", err)
			return
		}
		if err := b.helpdesk.SetTicketsMenuID(ctx, group, m.MessageID); err != nil {
			b.log.Warn("save tickets menu", "err", err)
		}
		if err := b.api.PinChatMessage(ctx, group, m.MessageID); err != nil {
			b.log.Debug("pin tickets menu", "err", err)
		}
		if old != 0 {
			if err := b.api.DeleteMessage(ctx, group, old); err != nil {
				b.log.Debug("delete old tickets menu", "err", err)
			}
		}
	}
	b.pruneTicketsTopic(ctx, group, topic)
}

// refreshTicketsMenu brings the counters of the pinned menu up to date.
func (b *Bot) refreshTicketsMenu(ctx context.Context) {
	group := b.helpdesk.GroupID()
	id := b.helpdesk.TicketsMenuID(ctx, group)
	if id == 0 {
		return
	}
	text, markup, err := b.ticketsMenuView(ctx)
	if err != nil {
		return
	}
	if err := b.editMenu(ctx, &msgRef{ChatID: group, MessageID: id}, text, markup); err != nil {
		b.log.Debug("refresh tickets menu", "err", err)
	}
}

// pruneTicketsTopic deletes the ticket cards that older versions posted into the tickets topic in
// addition to the user's topic. A card stays when its ticket has no other card (yet).
func (b *Bot) pruneTicketsTopic(ctx context.Context, group int64, topic int) {
	if b.helpdesk.TicketsPruned(ctx, group) {
		return
	}
	cards, err := b.helpdesk.CardsInTopic(ctx, group, topic)
	if err != nil {
		b.log.Warn("list duplicated ticket cards", "err", err)
		return
	}
	dropped := 0
	for _, c := range cards {
		all, err := b.helpdesk.Cards(ctx, c.TaskID)
		if err != nil || !hasCardElsewhere(all, topic) {
			continue
		}
		b.dropCard(ctx, c)
		dropped++
		select {
		case <-ctx.Done():
			return
		case <-time.After(70 * time.Millisecond): // stay far below Telegram's per-chat limits
		}
	}
	if err := b.helpdesk.MarkTicketsPruned(ctx, group); err != nil {
		b.log.Warn("mark tickets topic pruned", "err", err)
	}
	if dropped > 0 {
		b.log.Info("duplicated ticket cards removed from the tickets topic", "count", dropped)
	}
}

func hasCardElsewhere(cards []domain.HelpdeskCard, topic int) bool {
	for _, c := range cards {
		if c.TopicID != topic {
			return true
		}
	}
	return false
}

// ---------- morning digest ----------

const digestMaxLines = 25

// ageText renders how long ago something happened in the largest useful unit.
func ageText(d time.Duration) string {
	switch h := int(d.Hours()); {
	case h < 1:
		return "меньше часа"
	case h < 24:
		return fmt.Sprintf("%d ч", h)
	default:
		return fmt.Sprintf("%d дн", h/24)
	}
}

// TicketsDigest implements service.SchedulerNotifier: the open tickets go into the tickets topic
// every morning. Nothing is sent when no ticket needs attention.
func (b *Bot) TicketsDigest(ctx context.Context) {
	if !b.helpdesk.Active() {
		return
	}
	open, _, err := b.tasks.List(ctx, domain.TaskFilter{
		Statuses:     []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress},
		ConnectionID: b.botConn(), Limit: 200,
	})
	if err != nil {
		b.log.Error("tickets digest", "err", err)
		return
	}
	if len(open) == 0 {
		return
	}
	o, err := b.tasks.Overview(ctx, domain.ScopeHelpdesk, b.botConn())
	if err != nil {
		b.log.Error("tickets digest overview", "err", err)
		return
	}
	now := time.Now()
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🎫 Открытые тикеты</b> · %s\n", now.In(b.settings.Location()).Format("02.01.2006"))
	fmt.Fprintf(&sb, "Новые <b>%d</b> · В работе <b>%d</b> · Просрочено <b>%d</b>", o.New, o.InProgress, o.Overdue)
	if o.Snoozed > 0 {
		fmt.Fprintf(&sb, " · Отложено %d", o.Snoozed)
	}
	sb.WriteString("\n\n")
	for i, t := range open {
		if i == digestMaxLines {
			fmt.Fprintf(&sb, "\n…и ещё %d — полный список в закреплённом меню", len(open)-digestMaxLines)
			break
		}
		title := esc(trunc(t.Title, 70))
		if link := b.cardURL(ctx, t.ID); link != "" {
			title = fmt.Sprintf("<a href=\"%s\">%s</a>", esc(link), title)
		}
		fmt.Fprintf(&sb, "%s #%d %s — %s · %s · %s", priorityBars(t.Priority), t.ID, title, esc(trunc(t.SenderName, 24)),
			statusLabel(t.Status), ageText(now.Sub(t.CreatedAt)))
		if t.IsOverdue(now) {
			sb.WriteString(" · <b>просрочен</b>")
		}
		sb.WriteString("\n")
	}
	if _, _, _, err := b.sendToTicketsTopic(ctx, sb.String(), nil); err != nil {
		b.log.Warn("post tickets digest", "err", err)
		return
	}
	b.refreshTicketsMenu(ctx)
}
