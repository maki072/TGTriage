package tgbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/telegram"
)

func toDomainConnection(bc *telegram.BusinessConnection) *domain.BusinessConnection {
	return &domain.BusinessConnection{
		ID:              bc.ID,
		UserID:          bc.User.ID,
		UserChatID:      bc.UserChatID,
		UserName:        bc.User.FullName(),
		CanReply:        bc.Replyable(),
		CanReadMessages: bc.CanRead(),
		Enabled:         bc.IsEnabled,
	}
}

func (b *Bot) onBusinessConnection(ctx context.Context, bc *telegram.BusinessConnection) {
	c := toDomainConnection(bc)
	if !b.conns.IsOwner(c) {
		b.log.Warn("business connection from a non-owner account ignored", "user_id", bc.User.ID)
		return
	}
	if err := b.conns.Save(ctx, c); err != nil {
		b.log.Error("save business connection", "err", err)
		return
	}
	b.log.Info("business connection updated", "enabled", c.Enabled, "can_reply", c.CanReply, "can_read", c.CanReadMessages)

	var sb strings.Builder
	if !c.Enabled {
		sb.WriteString("<b>Бот отключён от Telegram Business</b>\nНовые сообщения больше не анализируются.")
	} else {
		sb.WriteString("<b>Бот подключён к Telegram Business</b>\n\n")
		if c.CanReply {
			sb.WriteString("Отправка ответов от вашего имени: разрешена\n")
		} else {
			sb.WriteString("Отправка ответов запрещена — кнопки «Ответить» работать не будут.\nВключите право «Отвечать на сообщения» в настройках чат-бота.\n")
		}
		if c.CanReadMessages {
			sb.WriteString("Отметка сообщений прочитанными: разрешена\n")
		} else {
			sb.WriteString("Отметка прочитанными недоступна (право «Читать сообщения» выключено)\n")
		}
		sb.WriteString("\nВходящие личные сообщения теперь анализируются автоматически.")
	}
	if err := b.sendText(ctx, sb.String(), kb(row(cb("Меню", "m")))); err != nil {
		b.log.Warn("notify about connection", "err", err)
	}
}

// connectionFor returns the stored connection or fetches it from Telegram (e.g. after DB loss).
func (b *Bot) connectionFor(ctx context.Context, id string) *domain.BusinessConnection {
	if id == "" {
		return nil
	}
	c, err := b.conns.Get(ctx, id)
	if err == nil {
		return c
	}
	if !errors.Is(err, domain.ErrNotFound) {
		b.log.Error("load business connection", "err", err)
		return nil
	}
	bc, err := b.api.GetBusinessConnection(ctx, id)
	if err != nil {
		b.log.Warn("getBusinessConnection failed", "err", err)
		return nil
	}
	c = toDomainConnection(bc)
	if b.conns.IsOwner(c) {
		if err := b.conns.Save(ctx, c); err != nil {
			b.log.Warn("save fetched business connection", "err", err)
		}
	}
	return c
}

func (b *Bot) onBusinessMessage(ctx context.Context, m *telegram.Message) {
	conn := b.connectionFor(ctx, m.BusinessConnectionID)
	if !b.conns.IsOwner(conn) {
		return
	}
	if m.Chat.Type != "private" || m.Chat.ID == conn.UserID {
		return
	}
	text := m.Content()
	if text == "" {
		return
	}
	outgoing := m.SenderBusinessBot != nil || (m.From != nil && m.From.ID == conn.UserID)
	dm := &domain.Message{
		ConnectionID: conn.ID,
		ChatID:       m.Chat.ID,
		MessageID:    m.MessageID,
		Text:         text,
		SentAt:       time.Unix(m.Date, 0),
		ViaBot:       m.SenderBusinessBot != nil,
	}
	if m.From != nil {
		dm.SenderID = m.From.ID
		dm.SenderName = m.From.FullName()
		dm.SenderUsername = m.From.Username
	}
	if dm.SenderName == "" {
		dm.SenderName = strings.TrimSpace(m.Chat.FirstName + " " + m.Chat.LastName)
		dm.SenderUsername = m.Chat.Username
	}

	var err error
	if outgoing {
		err = b.triage.OnOutgoing(ctx, dm)
		if err == nil && m.SenderBusinessBot == nil && m.From != nil && m.From.ID == conn.UserID {
			// typed by the owner (not a reply sent by the bot on their behalf)
			b.autoCloseOnDone(ctx, conn.ID, dm)
		}
	} else {
		if m.From != nil && m.From.IsBot {
			return
		}
		err = b.triage.OnIncoming(ctx, dm)
	}
	if err != nil {
		b.log.Error("store business message", "chat_id", dm.ChatID, "outgoing", outgoing, "err", err)
	}
}

func (b *Bot) onEditedBusinessMessage(ctx context.Context, m *telegram.Message) {
	conn := b.connectionFor(ctx, m.BusinessConnectionID)
	if !b.conns.IsOwner(conn) {
		return
	}
	if err := b.triage.OnEdited(ctx, conn.ID, m.Chat.ID, m.MessageID, m.Content()); err != nil {
		b.log.Error("update edited message", "err", err)
	}
}

func (b *Bot) onDeletedBusinessMessages(ctx context.Context, d *telegram.BusinessMessagesDeleted) {
	conn := b.connectionFor(ctx, d.BusinessConnectionID)
	if !b.conns.IsOwner(conn) {
		return
	}
	if err := b.triage.OnDeleted(ctx, conn.ID, d.Chat.ID, d.MessageIDs); err != nil {
		b.log.Error(fmt.Sprintf("mark %d messages deleted", len(d.MessageIDs)), "err", err)
	}
}
