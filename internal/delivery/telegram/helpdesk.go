package tgbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// ---------- users (private chat) ----------

func parseCommand(text string) (cmd, arg string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	cmd, arg, _ = strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(strings.ToLower(cmd), "@")
	return cmd, strings.TrimSpace(arg)
}

// sanitizeStartParam keeps a deep-link payload readable and bounded (Telegram allows [A-Za-z0-9_-]{1,64}).
func sanitizeStartParam(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return trunc(b.String(), 64)
}

func (b *Bot) onUserPrivate(ctx context.Context, m *telegram.Message) {
	if !b.helpdesk.Active() {
		b.log.Debug("message from a non-owner ignored: helpdesk is off", "user_id", m.From.ID)
		return
	}
	in := service.UserMessage{
		UserID: m.From.ID, Name: m.From.FullName(), Username: m.From.Username, LanguageCode: m.From.LanguageCode,
		MessageID: m.MessageID, MediaGroupID: m.MediaGroupID, Date: time.Unix(m.Date, 0),
	}
	if cmd, arg := parseCommand(m.Text); cmd == "/start" {
		in.Start, in.StartParam = true, sanitizeStartParam(arg)
	} else {
		in.Text = m.Content()
		if in.Text == "" {
			return // service message or unsupported content: nothing to relay
		}
		if r := m.ReplyToMessage; r != nil {
			in.ReplyToID = r.MessageID
		}
	}
	if err := b.helpdesk.OnUserMessage(ctx, in); err != nil {
		b.log.Error("helpdesk user message", "user_id", in.UserID, "err", err)
	}
}

// ---------- operators (private chat) ----------

const operatorHelp = `🎧 <b>Хелпдеск</b>

Вы оператор: пользователи пишут боту, их сообщения появляются в отдельных темах группы. Всё, что вы пишете в теме, бот отправляет пользователю от своего имени.

• <code>//</code> или <code>!</code> в начале — внутренняя заметка, пользователю не уходит
• ответ командой <code>/1</code> на сообщение пользователя — создать тикет
• перешлите сюда сообщение пользователя — тоже создастся тикет

Основная работа с тикетами — в веб-панели.`

func (b *Bot) onOperatorPrivate(ctx context.Context, m *telegram.Message) {
	if m.ForwardOrigin != nil {
		if !b.forwardToHelpdesk(ctx, m) {
			_ = b.sendTo(ctx, m.Chat.ID, "🤷 Не нашёл пользователя хелпдеска, от которого это сообщение. "+
				"Ответьте в его теме командой /1 на нужное сообщение.", nil)
		}
		return
	}
	cmd, arg := parseCommand(m.Text)
	if cmd == "/start" && strings.HasPrefix(arg, "t") {
		if id, err := strconv.ParseInt(arg[1:], 10, 64); err == nil {
			b.sendTicketLink(ctx, m.Chat.ID, id)
			return
		}
	}
	_ = b.sendTo(ctx, m.Chat.ID, operatorHelp, b.webAppKeyboard("📱 Открыть веб-панель", ""))
}

func (b *Bot) webAppKeyboard(label, query string) *telegram.InlineKeyboardMarkup {
	url := b.settings.Get().WebAppPublicURL
	if url == "" {
		return nil
	}
	return kb(row(webAppButton(label, url+query)))
}

func (b *Bot) sendTicketLink(ctx context.Context, chatID, taskID int64) {
	t, err := b.tasks.Get(ctx, taskID)
	if err != nil || !t.IsHelpdesk() {
		_ = b.sendTo(ctx, chatID, "❌ Тикет не найден", nil)
		return
	}
	text := fmt.Sprintf("🎫 <b>Тикет #%d</b> · %s\n%s", t.ID, esc(trunc(t.Title, 150)), statusLabel(t.Status))
	_ = b.sendTo(ctx, chatID, text, b.webAppKeyboard("📱 Открыть тикет", fmt.Sprintf("?task=%d", t.ID)))
}

// forwardToHelpdesk turns a message forwarded by the owner or an operator into a ticket when it came
// from a helpdesk user. Returns false when the author is not a helpdesk user.
func (b *Bot) forwardToHelpdesk(ctx context.Context, m *telegram.Message) bool {
	o := m.ForwardOrigin
	f := service.ForwardedMessage{OriginDate: time.Unix(o.Date, 0), Text: m.Content()}
	if o.SenderUser != nil {
		f.OriginUserID = o.SenderUser.ID
	}
	u, msg := b.helpdesk.ResolveForward(ctx, f)
	if u == nil {
		return false
	}
	chatID := m.Chat.ID
	b.helpdesk.QueueForwardTicket(chatID, u, *msg, func(ctx context.Context, t *domain.Task, err error) {
		if err != nil {
			_ = b.sendTo(ctx, chatID, "❌ Не удалось создать тикет: "+esc(humanError(err)), nil)
			return
		}
		text := fmt.Sprintf("🎫 <b>Тикет #%d создан</b> · %s\n👤 %s", t.ID, esc(trunc(t.Title, 150)), esc(u.Name))
		if link := b.helpdesk.TopicURL(u); link != "" {
			text += fmt.Sprintf("\n💬 <a href=\"%s\">Тема пользователя</a>", esc(link))
		}
		_ = b.sendTo(ctx, chatID, text, b.webAppKeyboard("📱 Открыть тикет", fmt.Sprintf("?task=%d", t.ID)))
	})
	if err := b.api.SendChatAction(ctx, chatID, "typing"); err != nil {
		b.log.Debug("sendChatAction failed", "err", err)
	}
	return true
}

// ---------- group ----------

// anonymousAdminID is Telegram's GroupAnonymousBot: messages of admins posting as the group.
const anonymousAdminID = 1087968824

func (b *Bot) onGroupMessage(ctx context.Context, m *telegram.Message) {
	group := m.Chat.ID
	switch {
	case m.ForumTopicClosed != nil:
		if err := b.helpdesk.OnTopicState(ctx, group, m.MessageThreadID, true); err != nil {
			b.log.Warn("sync closed topic", "err", err)
		}
		return
	case m.ForumTopicReopened != nil:
		if err := b.helpdesk.OnTopicState(ctx, group, m.MessageThreadID, false); err != nil {
			b.log.Warn("sync reopened topic", "err", err)
		}
		return
	case m.ForumTopicCreated != nil, m.ForumTopicEdited != nil:
		return
	}
	if m.From != nil && m.From.IsBot && m.From.ID != anonymousAdminID {
		return // our own copies and other bots
	}
	if !m.IsTopicMessage || m.MessageThreadID == 0 {
		return
	}
	raw := m.Text
	if raw == "" {
		raw = m.Caption
	}
	content := m.Content()
	if content == "" && !service.IsForceTicketCommand(raw) {
		return
	}
	in := service.OperatorMessage{
		GroupID: group, TopicID: m.MessageThreadID, MessageID: m.MessageID, MediaGroupID: m.MediaGroupID,
		RawText: raw, Text: content, Date: time.Unix(m.Date, 0),
	}
	if r := m.ReplyToMessage; r != nil && r.MessageID != m.MessageThreadID {
		in.ReplyToID, in.ReplyText = r.MessageID, r.Content()
	}
	switch {
	case m.From != nil && m.From.ID != anonymousAdminID:
		in.OperatorID, in.OperatorName = m.From.ID, m.From.FullName()
	case m.SenderChat != nil:
		in.OperatorName = m.SenderChat.Title
	}
	if err := b.helpdesk.OnOperatorMessage(ctx, in); err != nil {
		b.log.Error("helpdesk operator message", "topic_id", in.TopicID, "err", err)
	}
}

func editedText(m *telegram.Message) (string, json.RawMessage, bool) {
	if m.Text == "" {
		return m.Caption, m.CaptionEntities, true
	}
	return m.Text, m.Entities, false
}

func (b *Bot) onEditedMessage(ctx context.Context, m *telegram.Message) {
	text, entities, caption := editedText(m)
	var err error
	switch {
	case m.Chat.Type == "private" && m.From != nil && m.From.ID != b.cfg.OwnerID:
		err = b.helpdesk.OnUserEdited(ctx, m.Chat.ID, m.MessageID, text, entities, caption, m.Content())
	case m.Chat.ID != 0 && m.Chat.ID == b.helpdesk.GroupID():
		if service.IsInternalNote(text) {
			return
		}
		err = b.helpdesk.OnOperatorEdited(ctx, m.Chat.ID, m.MessageID, text, entities, caption, m.Content())
	}
	if err != nil {
		b.log.Warn("mirror edited message", "chat_id", m.Chat.ID, "err", err)
	}
}

func (b *Bot) onMyChatMember(ctx context.Context, u *telegram.ChatMemberUpdated) {
	switch {
	case u.Chat.Type == "private":
		blocked := u.NewChatMember.Status == "kicked"
		if err := b.helpdesk.OnUserBlocked(ctx, u.Chat.ID, blocked); err != nil {
			b.log.Warn("sync blocked user", "err", err)
		}
	case u.Chat.ID == b.helpdesk.ConfiguredGroupID():
		if !u.NewChatMember.InChat() {
			_ = b.sendText(ctx, "⚠️ <b>Бота удалили из группы хелпдеска</b> — сообщения пользователей больше не доставляются операторам.", nil)
		} else if u.NewChatMember.Status == "member" {
			_ = b.sendText(ctx, "⚠️ Бот в группе хелпдеска не администратор — назначьте его админом с правом «Управление темами».", nil)
		}
	case u.Chat.Type == "group" || u.Chat.Type == "supergroup":
		if u.NewChatMember.InChat() && u.NewChatMember.Status != u.OldChatMember.Status {
			b.offerHelpdeskGroup(ctx, u.Chat.ID, &u.From)
		}
	}
}

// offerHelpdeskGroup tells the owner the id of a group the bot was added to (or promoted in) and offers
// to use it for the helpdesk. Only the owner can press the button, so a stranger adding the bot to
// some group changes nothing.
func (b *Bot) offerHelpdeskGroup(ctx context.Context, chatID int64, by *telegram.User) {
	chat := telegram.Chat{ID: chatID}
	if ch, err := b.api.GetChat(ctx, chatID); err == nil {
		chat = *ch
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "👥 <b>Бот в группе «%s»</b>\nID: <code>%d</code>", esc(chat.Title), chat.ID)
	if by != nil && by.ID != 0 && !by.IsBot {
		fmt.Fprintf(&sb, "\nДобавил: %s", esc(by.FullName()))
	}
	if chat.Type == "group" {
		sb.WriteString("\n\n⚠️ Это обычная группа. Включите в ней «Темы» — Telegram превратит её в супергруппу с новым ID, и я пришлю его сюда.")
		_ = b.sendText(ctx, sb.String(), nil)
		return
	}
	if chat.ID == b.helpdesk.ConfiguredGroupID() {
		return
	}
	if !chat.IsForum {
		sb.WriteString("\n\n⚠️ В группе выключены «Темы» — включите их в настройках группы.")
	}
	if m, err := b.api.GetChatMember(ctx, chat.ID, b.cfg.BotID); err == nil && m.Status != "administrator" {
		sb.WriteString("\n⚠️ Бот не администратор — назначьте его админом с правами «Управление темами», «Закреплять» и «Удалять сообщения».")
	}
	markup := kb(row(cb("🎧 Использовать для хелпдеска", fmt.Sprintf("hg:%d", chat.ID))))
	if err := b.sendText(ctx, sb.String(), markup); err != nil {
		b.log.Warn("offer helpdesk group", "chat_id", chat.ID, "err", err)
	}
}

// useHelpdeskGroup sets the group for the helpdesk, enables it and reports the group check.
func (b *Bot) useHelpdeskGroup(ctx context.Context, ref *msgRef, groupID int64, answer func(string, bool)) error {
	if groupID >= 0 {
		return domain.ErrInvalidInput
	}
	if err := b.helpdesk.UpdateConfig(ctx, func(h *domain.HelpdeskSettings) {
		h.GroupID, h.Enabled = groupID, true
	}); err != nil {
		return err
	}
	answer("🎧 Хелпдеск включён", false)
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎧 <b>Хелпдеск включён</b>\nГруппа: <code>%d</code>\n\n", groupID)
	check, err := b.helpdesk.CheckGroup(ctx)
	if err != nil {
		fmt.Fprintf(&sb, "❌ Не удалось проверить группу: %s", esc(humanError(err)))
	} else {
		line := func(ok bool, text string) {
			mark := "✅"
			if !ok {
				mark = "❌"
			}
			fmt.Fprintf(&sb, "%s %s\n", mark, text)
		}
		line(true, "Группа: "+esc(check.Title))
		line(check.IsForum, "Темы включены")
		line(check.BotAdmin, "Бот — администратор")
		line(check.CanManageTopics, "Право «Управление темами»")
		line(check.CanPinMessages, "Право закреплять сообщения")
		line(check.CanDeleteMessages, "Право удалять сообщения")
		if check.IsForum && check.CanManageTopics {
			sb.WriteString("\nГотово: напишите боту с другого аккаунта — появится тема.")
		} else {
			sb.WriteString("\nИсправьте отмеченное ❌ — без тем и права «Управление темами» хелпдеск не работает.")
		}
	}
	markup := b.webAppKeyboard("⚙️ Настройки хелпдеска", "")
	return b.render(ctx, ref, sb.String(), markup)
}

// ---------- ticket cards ----------

func (b *Bot) ticketCardText(t *domain.Task, u *domain.HelpdeskUser, withTopicLink bool) string {
	now := time.Now()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🎫 %s <b>Тикет #%d · %s</b>\n", priorityEmoji(t.Priority), t.ID, esc(trunc(t.Title, 150)))
	status := statusLabel(t.Status)
	if t.Status == domain.StatusSnoozed && t.SnoozeUntil != nil {
		status += " до " + b.fmtShort(*t.SnoozeUntil)
	}
	fmt.Fprintf(&sb, "Статус: %s\n\n", status)
	name := t.SenderName
	if u != nil {
		name = u.Name
	}
	fmt.Fprintf(&sb, "👤 %s", esc(trunc(name, 60)))
	if t.SenderUsername != "" {
		fmt.Fprintf(&sb, " (@%s)", esc(t.SenderUsername))
	}
	sb.WriteString("\n")
	if u != nil {
		if u.Source != "" {
			fmt.Fprintf(&sb, "🔗 Источник: <code>%s</code>\n", esc(u.Source))
		}
		if link := b.helpdesk.TopicURL(u); withTopicLink && link != "" {
			fmt.Fprintf(&sb, "💬 <a href=\"%s\">Тема пользователя</a>\n", esc(link))
		}
		if u.Blocked {
			sb.WriteString("🚫 Пользователь заблокировал бота\n")
		}
	}
	fmt.Fprintf(&sb, "🏷 %s · ⚡ %s\n", categoryLabel(t.Category), priorityName(t.Priority))
	if t.Deadline != nil {
		d := b.fmtTime(*t.Deadline)
		if t.IsOverdue(now) {
			d += " ⚠️ <b>просрочено</b>"
		}
		fmt.Fprintf(&sb, "📅 Срок: %s\n", d)
	}
	fmt.Fprintf(&sb, "🕒 Создан: %s\n", b.fmtTime(t.CreatedAt))
	if t.Description != "" {
		fmt.Fprintf(&sb, "\n📝 %s\n", esc(trunc(t.Description, 1000)))
	}
	if t.SourceText != "" {
		fmt.Fprintf(&sb, "\n<blockquote expandable>%s</blockquote>\n", esc(trunc(t.SourceText, 1200)))
	}
	if t.DraftReply != "" && t.ReplySentAt == nil {
		fmt.Fprintf(&sb, "\n✍️ <b>Черновик ответа:</b>\n<i>%s</i>\n", esc(trunc(t.DraftReply, 600)))
	}
	if t.ReplySentAt != nil {
		fmt.Fprintf(&sb, "\n📤 Ответ отправлен %s\n", b.fmtShort(*t.ReplySentAt))
	}
	return sb.String()
}

func (b *Bot) ticketKeyboard(t *domain.Task, u *domain.HelpdeskUser, withTopicLink bool) *telegram.InlineKeyboardMarkup {
	d := func(action string) string { return fmt.Sprintf("hc:%s:%d", action, t.ID) }
	var rows [][]button
	if t.Status.IsOpen() {
		var r1 []button
		if t.DraftReply != "" && t.ReplySentAt == nil && t.HasChat() {
			r1 = append(r1, cb("🚀 Ответить черновиком", d("draft")))
		}
		if t.Status != domain.StatusInProgress {
			r1 = append(r1, cb("👀 В работу", d("work")))
		}
		if len(r1) > 0 {
			rows = append(rows, r1)
		}
		rows = append(rows, row(cb("✅ Закрыть", d("done")), cb("🗑 Ошибка", d("fp"))))
	} else {
		rows = append(rows, row(cb("♻️ Вернуть в работу", d("reopen"))))
	}
	var links []button
	if link := b.helpdesk.TopicURL(u); withTopicLink && link != "" && u != nil {
		links = append(links, button{Text: "💬 Тема", URL: link})
	}
	if b.settings.Get().WebAppPublicURL != "" && b.cfg.BotUsername != "" {
		links = append(links, button{Text: "📱 В панели", URL: fmt.Sprintf("https://t.me/%s?start=t%d", b.cfg.BotUsername, t.ID)})
	}
	if len(links) > 0 {
		rows = append(rows, links)
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) ticketUser(ctx context.Context, t *domain.Task) *domain.HelpdeskUser {
	if !t.HasChat() {
		return nil
	}
	u, err := b.helpdesk.User(ctx, t.ChatID)
	if err != nil {
		return nil
	}
	return u
}

// publishTicket posts ticket cards into the user's topic (pinned) and the tickets topic, or updates
// the cards posted before.
func (b *Bot) publishTicket(ctx context.Context, t *domain.Task) {
	if !b.helpdesk.Active() {
		return
	}
	u := b.ticketUser(ctx, t)
	cards, err := b.helpdesk.Cards(ctx, t.ID)
	if err != nil {
		b.log.Error("load ticket cards", "task_id", t.ID, "err", err)
		return
	}
	group := b.helpdesk.GroupID()
	if len(cards) > 0 {
		for _, c := range cards {
			inTickets := u == nil || c.TopicID != u.TopicID
			err := b.api.EditMessageText(ctx, telegram.EditMessageTextParams{
				ChatID: c.ChatID, MessageID: c.MessageID, Text: b.ticketCardText(t, u, inTickets), ParseMode: "HTML",
				ReplyMarkup: b.ticketKeyboard(t, u, inTickets), LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
			})
			if err != nil && !telegram.IsNotModified(err) {
				b.log.Debug("update ticket card", "task_id", t.ID, "err", err)
			}
		}
		return
	}

	post := func(topicID int, withLink bool) (int, error) {
		m, err := b.api.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: group, MessageThreadID: topicID, Text: b.ticketCardText(t, u, withLink), ParseMode: "HTML",
			ReplyMarkup: b.ticketKeyboard(t, u, withLink), LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
		})
		if err != nil {
			return 0, err
		}
		if err := b.helpdesk.SaveCard(ctx, domain.HelpdeskCard{TaskID: t.ID, ChatID: group, TopicID: topicID, MessageID: m.MessageID}); err != nil {
			b.log.Warn("save ticket card", "err", err)
		}
		return m.MessageID, nil
	}
	if u != nil && u.HasTopic(group) {
		if id, err := post(u.TopicID, false); err != nil {
			b.log.Warn("post ticket card to user topic", "task_id", t.ID, "err", err)
		} else if err := b.api.PinChatMessage(ctx, group, id); err != nil {
			b.log.Debug("pin ticket card", "err", err)
		}
	}
	_, ticketsTopic, err := b.helpdesk.TicketsTopic(ctx)
	if err != nil {
		b.log.Warn("tickets topic", "err", err)
		return
	}
	if _, err := post(ticketsTopic, true); err != nil {
		if !telegram.IsTopicGone(err) {
			b.log.Warn("post ticket card to tickets topic", "task_id", t.ID, "err", err)
			return
		}
		b.helpdesk.ForgetTicketsTopic(ctx, group)
		if _, ticketsTopic, err = b.helpdesk.TicketsTopic(ctx); err == nil {
			if _, err := post(ticketsTopic, true); err != nil {
				b.log.Warn("post ticket card to recreated tickets topic", "task_id", t.ID, "err", err)
			}
		}
	}
}

// topicNotice posts a short note into the ticket user's topic.
func (b *Bot) topicNotice(ctx context.Context, t *domain.Task, text string) {
	u := b.ticketUser(ctx, t)
	group := b.helpdesk.GroupID()
	if u == nil || !u.HasTopic(group) {
		return
	}
	if _, err := b.api.SendMessage(ctx, telegram.SendMessageParams{ChatID: group, MessageThreadID: u.TopicID,
		Text: text, ParseMode: "HTML", DisableNotification: true}); err != nil {
		b.log.Debug("post topic notice", "err", err)
	}
}

// groupCallback handles ticket card buttons pressed by operators in the helpdesk group.
func (b *Bot) groupCallback(ctx context.Context, p []string, answer func(string, bool)) error {
	if len(p) < 3 || p[0] != "hc" {
		return nil
	}
	id, err := strconv.ParseInt(p[2], 10, 64)
	if err != nil {
		return domain.ErrInvalidInput
	}
	t, err := b.tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if !t.IsHelpdesk() {
		return domain.ErrForbidden
	}
	var notice string
	switch p[1] {
	case "draft":
		_, err = b.tasks.SendDraft(ctx, id)
		notice = "🚀 Черновик отправлен пользователю"
	case "work":
		_, err = b.tasks.SetStatus(ctx, id, domain.StatusInProgress)
		notice = "👀 Взято в работу"
	case "done":
		_, err = b.tasks.SetStatus(ctx, id, domain.StatusDone)
		notice = "✅ Тикет закрыт"
	case "fp":
		_, err = b.tasks.SetStatus(ctx, id, domain.StatusFalsePositive)
		notice = "🗑 Отмечено как ошибка"
	case "reopen":
		_, err = b.tasks.SetStatus(ctx, id, domain.StatusNew)
		notice = "♻️ Тикет возвращён"
	default:
		return domain.ErrInvalidInput
	}
	if err != nil {
		return err
	}
	answer(notice, false)
	return nil
}
