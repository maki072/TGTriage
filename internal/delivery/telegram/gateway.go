package tgbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// Gateway adapts the Bot API client to the service ports: Business replies, the support desk
// transport and backup delivery.
type Gateway struct {
	api     *telegram.Client
	ownerID int64
	botID   int64
}

var (
	_ service.ReplySender       = (*Gateway)(nil)
	_ service.HelpdeskTransport = (*Gateway)(nil)
	_ service.BackupSender      = (*Gateway)(nil)
)

func NewGateway(api *telegram.Client, ownerID, botID int64) *Gateway {
	return &Gateway{api: api, ownerID: ownerID, botID: botID}
}

func (g *Gateway) SendBusinessText(ctx context.Context, connectionID string, chatID int64, text string) (int, error) {
	m, err := g.api.SendMessage(ctx, telegram.SendMessageParams{
		BusinessConnectionID: connectionID,
		ChatID:               chatID,
		Text:                 text,
	})
	if err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

func (g *Gateway) MarkRead(ctx context.Context, connectionID string, chatID int64, messageID int) error {
	return g.api.ReadBusinessMessage(ctx, connectionID, chatID, messageID)
}

// wrap translates Bot API errors into domain errors. A 403 means "blocked" only for a user's chat:
// in the group it means the bot lost access, which is reported as is.
func wrap(chatID int64, err error) error {
	switch {
	case err == nil:
		return nil
	case telegram.IsTopicGone(err):
		return fmt.Errorf("%w: %v", domain.ErrTopicGone, err)
	case telegram.IsTopicClosed(err):
		return fmt.Errorf("%w: %v", domain.ErrTopicClosed, err)
	case chatID > 0 && telegram.IsForbidden(err):
		return fmt.Errorf("%w: %v", domain.ErrUserBlocked, err)
	}
	return err
}

func isNotModified(err error) bool {
	var ae *telegram.APIError
	return errors.As(err, &ae) && (strings.Contains(ae.Description, "NOT_MODIFIED") || strings.Contains(ae.Description, "not modified"))
}

func (g *Gateway) CreateTopic(ctx context.Context, groupID int64, name string) (int, error) {
	t, err := g.api.CreateForumTopic(ctx, groupID, name)
	if err != nil {
		return 0, wrap(groupID, err)
	}
	return t.MessageThreadID, nil
}

func (g *Gateway) EditTopic(ctx context.Context, groupID int64, topicID int, name string) error {
	if err := g.api.EditForumTopic(ctx, groupID, topicID, name); err != nil && !isNotModified(err) {
		return wrap(groupID, err)
	}
	return nil
}

func (g *Gateway) CloseTopic(ctx context.Context, groupID int64, topicID int) error {
	if err := g.api.CloseForumTopic(ctx, groupID, topicID); err != nil && !isNotModified(err) {
		return wrap(groupID, err)
	}
	return nil
}

func (g *Gateway) ReopenTopic(ctx context.Context, groupID int64, topicID int) error {
	if err := g.api.ReopenForumTopic(ctx, groupID, topicID); err != nil && !isNotModified(err) {
		return wrap(groupID, err)
	}
	return nil
}

func (g *Gateway) Copy(ctx context.Context, fromChatID int64, ids []int, toChatID int64, topicID, replyTo int) ([]int, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) == 1 {
		p := telegram.CopyMessageParams{ChatID: toChatID, MessageThreadID: topicID, FromChatID: fromChatID, MessageID: ids[0]}
		if replyTo != 0 {
			p.ReplyParameters = &telegram.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
		}
		id, err := g.api.CopyMessage(ctx, p)
		if err != nil {
			return nil, wrap(toChatID, err)
		}
		return []int{id}, nil
	}
	out, err := g.api.CopyMessages(ctx, toChatID, topicID, fromChatID, ids)
	return out, wrap(toChatID, err)
}

func (g *Gateway) SendHTML(ctx context.Context, chatID int64, topicID int, text string, replyTo int) (int, error) {
	p := telegram.SendMessageParams{ChatID: chatID, MessageThreadID: topicID, Text: text, ParseMode: "HTML",
		LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true}}
	if replyTo != 0 {
		p.ReplyParameters = &telegram.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}
	m, err := g.api.SendMessage(ctx, p)
	if err != nil {
		return 0, wrap(chatID, err)
	}
	return m.MessageID, nil
}

func (g *Gateway) EditText(ctx context.Context, chatID int64, messageID int, text string, entities json.RawMessage, caption bool) error {
	var err error
	if caption {
		err = g.api.EditMessageCaption(ctx, telegram.EditMessageCaptionParams{ChatID: chatID, MessageID: messageID,
			Caption: text, CaptionEntities: entities})
	} else {
		err = g.api.EditMessageText(ctx, telegram.EditMessageTextParams{ChatID: chatID, MessageID: messageID,
			Text: text, Entities: entities})
	}
	if err != nil && !telegram.IsNotModified(err) {
		return wrap(chatID, err)
	}
	return nil
}

func (g *Gateway) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return wrap(chatID, g.api.DeleteMessage(ctx, chatID, messageID))
}

func (g *Gateway) IsChatMember(ctx context.Context, chatID, userID int64) (bool, error) {
	m, err := g.api.GetChatMember(ctx, chatID, userID)
	if err != nil {
		var ae *telegram.APIError
		if errors.As(err, &ae) && ae.Code == 400 {
			return false, nil // user never was in the chat
		}
		return false, err
	}
	return m.InChat(), nil
}

func (g *Gateway) CheckGroup(ctx context.Context, groupID int64) (*service.GroupCheck, error) {
	ch, err := g.api.GetChat(ctx, groupID)
	if err != nil {
		return nil, err
	}
	res := &service.GroupCheck{Title: ch.Title, IsForum: ch.IsForum}
	m, err := g.api.GetChatMember(ctx, groupID, g.botID)
	if err != nil {
		return res, nil
	}
	switch m.Status {
	case "creator":
		res.BotAdmin, res.CanManageTopics, res.CanDeleteMessages, res.CanPinMessages = true, true, true, true
	case "administrator":
		res.BotAdmin, res.CanManageTopics = true, m.CanManageTopics
		res.CanDeleteMessages, res.CanPinMessages = m.CanDeleteMessages, m.CanPinMessages
	}
	return res, nil
}

// SendBackup uploads a backup file to the owner's chat with the bot.
func (g *Gateway) SendBackup(ctx context.Context, path, caption string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return g.api.SendDocument(ctx, g.ownerID, filepath.Base(path), f, caption)
}

// spamCallback is the callback data of the "ban as spam" (ban=true) / "unban" button on a user's card.
func spamCallback(userID int64, ban bool) string {
	if ban {
		return fmt.Sprintf("hb:ban:%d", userID)
	}
	return fmt.Sprintf("hb:unban:%d", userID)
}

func spamKeyboard(userID int64, banned bool) *telegram.InlineKeyboardMarkup {
	if banned {
		return kb(row(cb("✅ Разбанить", spamCallback(userID, false))))
	}
	return kb(row(cb("🚫 Спам — забанить", spamCallback(userID, true))))
}

func (g *Gateway) SendUserHeader(ctx context.Context, groupID int64, topicID int, text string, userID int64) (int, error) {
	m, err := g.api.SendMessage(ctx, telegram.SendMessageParams{ChatID: groupID, MessageThreadID: topicID, Text: text,
		ParseMode: "HTML", ReplyMarkup: spamKeyboard(userID, false), LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true}})
	if err != nil {
		return 0, wrap(groupID, err)
	}
	return m.MessageID, nil
}
