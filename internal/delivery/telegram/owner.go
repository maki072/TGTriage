package tgbot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

const helpText = `<b>Персональный ассистент на Telegram Business</b>

Бот читает входящие личные сообщения вашего аккаунта, склеивает серии коротких сообщений, отсекает шум с помощью LLM и превращает запросы собеседников в задачи с черновиком ответа.

<b>Команды</b>
/menu — главное меню
/tasks — активные задачи
/urgent — срочные задачи
/task N — карточка задачи #N
/digest — дайджест висящих и просроченных задач
/stats — статистика качества триажа
/settings — ключи AI, модели, дебаунс, чувствительность, дайджест
/cancel — отменить ввод

<b>Закрытие по вашему слову</b>
Если вы сами напишете собеседнику короткое «готово», «сделал», «готово, отключился», открытая задача из этого чата закроется (если их несколько — бот спросит, какую). Включается в /settings → «Ещё настройки».

<b>Пересылка</b>
Перешлите боту сообщение (или сразу несколько) из любого чата — чужое или своё, — и оно станет задачей. Сообщение пользователя хелпдеска станет тикетом.

<b>Хелпдеск</b>
Пользователи пишут боту, операторы отвечают в темах супергруппы. Настройка — в веб-панели.

<b>Подключение</b>
Telegram → Настройки → Telegram Business → Чат-боты → укажите этого бота и разрешите «Отвечать на сообщения» (и «Читать сообщения» — для отметки прочитанным).`

func (b *Bot) onPrivateMessage(ctx context.Context, m *telegram.Message) {
	if m.From == nil {
		return
	}
	ctx = service.WithActor(ctx, service.Actor{ID: m.From.ID, Name: m.From.FullName()})
	switch {
	case m.From.ID == b.cfg.OwnerID:
		b.onOwnerMessage(ctx, m)
	case b.helpdesk.Active() && b.helpdesk.IsOperator(ctx, m.From.ID):
		b.onOperatorPrivate(ctx, m)
	default:
		b.onUserPrivate(ctx, m)
	}
}

func (b *Bot) onOwnerMessage(ctx context.Context, m *telegram.Message) {
	if m.ForwardOrigin != nil {
		if !b.forwardToHelpdesk(ctx, m) {
			b.onForward(ctx, m)
		}
		return
	}
	if cmd, arg := parseCommand(m.Text); cmd == "/start" && strings.HasPrefix(arg, "t") {
		if id, err := strconv.ParseInt(arg[1:], 10, 64); err == nil {
			b.sendTicketLink(ctx, m.Chat.ID, id)
			return
		}
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		b.onCommand(ctx, text)
		return
	}
	if st, ok := b.states.get(); ok {
		b.onInput(ctx, st, text)
		return
	}
	if err := b.showMainMenu(ctx, nil); err != nil {
		b.log.Warn("show menu", "err", err)
	}
}

// onForward hands a message the owner forwarded from another chat to triage, which turns it into a task.
// A multi-message forward arrives as separate updates; the service glues them into one batch.
func (b *Bot) onForward(ctx context.Context, m *telegram.Message) {
	text := m.Content()
	if text == "" {
		return
	}
	o := m.ForwardOrigin
	dm := domain.Message{MessageID: m.MessageID, SenderName: o.AuthorName(), Text: text, SentAt: time.Unix(o.Date, 0)}
	if o.SenderUser != nil {
		dm.SenderID = o.SenderUser.ID
		dm.SenderUsername = o.SenderUser.Username
		dm.Outgoing = o.SenderUser.ID == b.cfg.OwnerID
	} else if c := o.AuthorChat(); c != nil {
		dm.SenderUsername = c.Username
	}
	b.triage.OnForwarded(dm)
	if err := b.api.SendChatAction(ctx, b.cfg.OwnerID, "typing"); err != nil {
		b.log.Debug("sendChatAction failed", "err", err)
	}
}

func (b *Bot) onCommand(ctx context.Context, text string) {
	cmd, arg, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(strings.ToLower(cmd), "@")
	arg = strings.TrimSpace(arg)

	var err error
	switch cmd {
	case "/start", "/menu":
		b.states.clear()
		err = b.showMainMenu(ctx, nil)
	case "/tasks":
		err = b.showList(ctx, nil, defaultListQuery())
	case "/urgent":
		err = b.showList(ctx, nil, listQuery{Status: "act", Prio: "urg"})
	case "/task":
		id, perr := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
		if perr != nil || id <= 0 {
			err = b.sendText(ctx, "Использование: <code>/task 12</code>", nil)
			break
		}
		err = b.showTask(ctx, nil, id)
	case "/settings":
		b.states.clear()
		err = b.showSettings(ctx, nil)
	case "/digest":
		err = b.sendDigest(ctx)
	case "/stats":
		err = b.showStats(ctx, nil)
	case "/cancel":
		b.states.clear()
		err = b.sendText(ctx, "Ввод отменён", kb(row(cb("Меню", "m"))))
	case "/help":
		err = b.sendText(ctx, helpText, kb(row(cb("Меню", "m"))))
	default:
		err = b.sendText(ctx, "Неизвестная команда. /help — справка", nil)
	}
	if err != nil {
		b.log.Warn("command failed", "cmd", cmd, "err", err)
		_ = b.sendText(ctx, "Ошибка: "+esc(humanError(err)), nil)
	}
}

func (b *Bot) onInput(ctx context.Context, st dialogState, text string) {
	var err error
	switch st.Kind {
	case stateReply:
		err = b.inputReply(ctx, st, text)
	case stateSnoozeCustom:
		err = b.inputSnooze(ctx, st, text)
	case stateModel:
		if !service.ValidModelName(text) {
			err = fmt.Errorf("%w: некорректный идентификатор модели", domain.ErrInvalidInput)
			break
		}
		if err = b.setModel(ctx, st.Provider, text); err == nil {
			b.states.clear()
			err = b.showSettings(ctx, nil)
		}
	case stateDebounce:
		sec, perr := strconv.Atoi(text)
		if perr != nil {
			err = fmt.Errorf("%w: нужно целое число секунд", domain.ErrInvalidInput)
			break
		}
		if _, err = b.settings.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = sec }); err == nil {
			b.states.clear()
			err = b.showSettings(ctx, nil)
		}
	case stateDigestTime:
		h, m, perr := service.ParseClock(text)
		if perr != nil {
			err = perr
			break
		}
		if _, err = b.settings.Update(ctx, func(s *domain.Settings) { s.DigestTime = fmt.Sprintf("%02d:%02d", h, m) }); err == nil {
			b.states.clear()
			err = b.showSettings(ctx, nil)
		}
	default:
		b.states.clear()
	}
	if err != nil {
		// keep the state on validation errors so the owner can just retype
		if !errors.Is(err, domain.ErrInvalidInput) && !errors.Is(err, domain.ErrEmptyReply) {
			b.states.clear()
		}
		if serr := b.sendText(ctx, "Ошибка: "+esc(humanError(err)), kb(row(cb("Отмена", "cx")))); serr != nil {
			b.log.Warn("send input error", "err", serr)
		}
	}
}

func (b *Bot) inputReply(ctx context.Context, st dialogState, text string) error {
	t, err := b.tasks.SendReply(ctx, st.TaskID, text)
	if err != nil {
		return err
	}
	b.states.clear()
	return b.renderTask(ctx, nil, t, fmt.Sprintf("<b>Ответ отправлен</b> собеседнику %s", esc(t.SenderName)), nil)
}

func (b *Bot) inputSnooze(ctx context.Context, st dialogState, text string) error {
	until, err := service.ParseWhen(text, time.Now(), b.settings.Location())
	if err != nil {
		return err
	}
	t, err := b.tasks.Snooze(ctx, st.TaskID, until)
	if err != nil {
		return err
	}
	b.states.clear()
	return b.renderTask(ctx, nil, t, "<b>Отложено до "+b.fmtTime(until)+"</b>", nil)
}
