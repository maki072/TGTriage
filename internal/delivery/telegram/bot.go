// Package tgbot is the Telegram delivery layer: it routes Bot API updates to use cases, renders the
// owner's task-manager interface and relays the support desk between users and the operators' group.
package tgbot

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// Config of the delivery layer.
type Config struct {
	OwnerID     int64
	BotID       int64
	BotUsername string
	// BotDBID is 0 for the main bot (from .env), or the row id in the bots table for an
	// additional, client-organization bot added from the Mini App.
	BotDBID int64
}

// Bot handles updates and implements service.Notifier, service.SchedulerNotifier and service.TaskObserver.
type Bot struct {
	api      *telegram.Client
	cfg      Config
	tasks    *service.TaskService
	triage   *service.TriageService
	settings *service.SettingsService
	conns    *service.ConnectionService
	helpdesk *service.HelpdeskService
	states   *stateStore
	log      *slog.Logger
}

var (
	_ service.Notifier          = (*Bot)(nil)
	_ service.SchedulerNotifier = (*Bot)(nil)
	_ service.TaskObserver      = (*Bot)(nil)
)

func New(api *telegram.Client, cfg Config, tasks *service.TaskService, settings *service.SettingsService,
	conns *service.ConnectionService, helpdesk *service.HelpdeskService, log *slog.Logger) *Bot {
	return &Bot{
		api: api, cfg: cfg, tasks: tasks, settings: settings, conns: conns, helpdesk: helpdesk,
		states: newStateStore(), log: log.With("component", "bot"),
	}
}

// SetTriage wires the triage service (it depends on Bot as a notifier).
func (b *Bot) SetTriage(t *service.TriageService) { b.triage = t }

var ownerCommands = []telegram.BotCommand{
	{Command: "menu", Description: "Главное меню"},
	{Command: "tasks", Description: "Активные задачи"},
	{Command: "urgent", Description: "Срочные задачи"},
	{Command: "digest", Description: "Дайджест висящих задач"},
	{Command: "stats", Description: "Статистика качества триажа"},
	{Command: "settings", Description: "Настройки"},
	{Command: "cancel", Description: "Отменить ввод"},
	{Command: "help", Description: "Справка"},
}

// Everyone else — support desk users — only needs /start; owner commands are scoped to the owner's chat.
var publicCommands = []telegram.BotCommand{{Command: "start", Description: "Начать"}}

// Run starts long polling; blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	if err := b.api.SetMyCommands(ctx, publicCommands); err != nil {
		b.log.Warn("setMyCommands failed", "err", err)
	}
	if err := b.api.SetMyCommandsForChat(ctx, b.cfg.OwnerID, ownerCommands); err != nil {
		b.log.Warn("setMyCommands for owner failed", "err", err)
	}
	b.EnsureMenuButton(ctx)
	b.api.Poll(ctx, b.handle)
}

// EnsureMenuButton points the persistent menu button (next to the message box, in every private
// chat) at the Mini App, so users open it directly instead of hunting for a command or a button
// buried in some message. Called on start and again whenever WebAppPublicURL changes.
func (b *Bot) EnsureMenuButton(ctx context.Context) {
	url := b.settings.Get().WebAppPublicURL
	if url == "" {
		return
	}
	mb := &telegram.MenuButton{Type: "web_app", Text: "Открыть панель", WebApp: &telegram.WebAppInfo{URL: url}}
	if err := b.api.SetChatMenuButton(ctx, 0, mb); err != nil {
		b.log.Warn("setChatMenuButton failed", "err", err)
	}
}

func (b *Bot) handle(ctx context.Context, u telegram.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in update handler", "panic", r, "update_id", u.UpdateID, "stack", string(debug.Stack()))
		}
	}()
	hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	switch {
	case u.BusinessConnection != nil:
		b.onBusinessConnection(hctx, u.BusinessConnection)
	case u.BusinessMessage != nil:
		b.onBusinessMessage(hctx, u.BusinessMessage)
	case u.EditedBusinessMessage != nil:
		b.onEditedBusinessMessage(hctx, u.EditedBusinessMessage)
	case u.DeletedBusinessMessages != nil:
		b.onDeletedBusinessMessages(hctx, u.DeletedBusinessMessages)
	case u.CallbackQuery != nil:
		b.onCallback(hctx, u.CallbackQuery)
	case u.MyChatMember != nil:
		b.onMyChatMember(hctx, u.MyChatMember)
	case u.EditedMessage != nil:
		b.onEditedMessage(hctx, u.EditedMessage)
	case u.Message != nil:
		b.onMessage(hctx, u.Message)
	}
}

func (b *Bot) onMessage(ctx context.Context, m *telegram.Message) {
	switch {
	case m.MigrateToChatID != 0:
		// enabling topics turns a basic group into a supergroup with a new id
		b.offerHelpdeskGroup(ctx, m.MigrateToChatID, m.From)
	case m.Chat.Type == "private":
		b.onPrivateMessage(ctx, m)
	case m.Chat.ID != 0 && m.Chat.ID == b.helpdesk.GroupID():
		b.onGroupMessage(ctx, m)
	}
}

// msgRef points to a bot message that should be edited in place.
type msgRef struct {
	ChatID    int64
	MessageID int
}

// render edits the referenced message or sends a new one to the owner.
func (b *Bot) render(ctx context.Context, ref *msgRef, text string, markup *telegram.InlineKeyboardMarkup) error {
	noPreview := &telegram.LinkPreviewOptions{IsDisabled: true}
	if ref != nil {
		err := b.api.EditMessageText(ctx, telegram.EditMessageTextParams{
			ChatID: ref.ChatID, MessageID: ref.MessageID, Text: text, ParseMode: "HTML",
			ReplyMarkup: markup, LinkPreviewOptions: noPreview,
		})
		if err == nil || telegram.IsNotModified(err) {
			return nil
		}
		if telegram.IsEntityError(err) {
			return err
		}
		b.log.Debug("edit failed, sending a new message", "err", err)
	}
	return b.sendTo(ctx, b.cfg.OwnerID, text, markup)
}

// sendTo sends an HTML message to any private chat.
func (b *Bot) sendTo(ctx context.Context, chatID int64, text string, markup *telegram.InlineKeyboardMarkup) error {
	_, err := b.api.SendMessage(ctx, telegram.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: "HTML", ReplyMarkup: markup,
		LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
	})
	return err
}

func (b *Bot) sendText(ctx context.Context, text string, markup *telegram.InlineKeyboardMarkup) error {
	return b.render(ctx, nil, text, markup)
}

func (b *Bot) renderError(ctx context.Context, ref *msgRef, err error) error {
	return b.render(ctx, ref, "❌ "+esc(humanError(err)), kb(row(cb("🏠 Меню", "m"))))
}
