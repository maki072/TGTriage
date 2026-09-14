// Package tgbot is the Telegram delivery layer: it routes Bot API updates to use cases
// and renders the owner's task-manager interface (inline keyboards, cards, settings).
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
	OwnerID           int64
	Location          *time.Location
	ClaudePresets     []string
	GeminiPresets     []string
	GroqPresets       []string
	MistralPresets    []string
	OpenRouterPresets []string
	DeepLinks         bool   // add tg:// links to the source message in task cards
	WebAppURL         string // public https:// URL of the Mini App; empty hides the "🌐 Открыть веб-панель" button
}

// Bot handles updates and implements service.Notifier and service.SchedulerNotifier.
type Bot struct {
	api      *telegram.Client
	cfg      Config
	tasks    *service.TaskService
	triage   *service.TriageService
	settings *service.SettingsService
	conns    *service.ConnectionService
	states   *stateStore
	log      *slog.Logger
}

var (
	_ service.Notifier          = (*Bot)(nil)
	_ service.SchedulerNotifier = (*Bot)(nil)
)

func New(api *telegram.Client, cfg Config, tasks *service.TaskService, settings *service.SettingsService,
	conns *service.ConnectionService, log *slog.Logger) *Bot {
	return &Bot{
		api: api, cfg: cfg, tasks: tasks, settings: settings, conns: conns,
		states: newStateStore(), log: log.With("component", "bot"),
	}
}

// SetTriage wires the triage service (it depends on Bot as a notifier).
func (b *Bot) SetTriage(t *service.TriageService) { b.triage = t }

var commands = []telegram.BotCommand{
	{Command: "menu", Description: "Главное меню"},
	{Command: "tasks", Description: "Активные задачи"},
	{Command: "urgent", Description: "Срочные задачи"},
	{Command: "digest", Description: "Дайджест висящих задач"},
	{Command: "stats", Description: "Статистика качества триажа"},
	{Command: "settings", Description: "Настройки"},
	{Command: "cancel", Description: "Отменить ввод"},
	{Command: "help", Description: "Справка"},
}

// Run starts long polling; blocks until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	if err := b.api.SetMyCommands(ctx, commands); err != nil {
		b.log.Warn("setMyCommands failed", "err", err)
	}
	b.api.Poll(ctx, b.handle)
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
	case u.Message != nil:
		b.onPrivateMessage(hctx, u.Message)
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
	_, err := b.api.SendMessage(ctx, telegram.SendMessageParams{
		ChatID: b.cfg.OwnerID, Text: text, ParseMode: "HTML", ReplyMarkup: markup, LinkPreviewOptions: noPreview,
	})
	return err
}

func (b *Bot) sendText(ctx context.Context, text string, markup *telegram.InlineKeyboardMarkup) error {
	return b.render(ctx, nil, text, markup)
}

func (b *Bot) renderError(ctx context.Context, ref *msgRef, err error) error {
	return b.render(ctx, ref, "❌ "+esc(humanError(err)), kb(row(cb("🏠 Меню", "m"))))
}

// Gateway adapts the Bot API client to service.ReplySender.
type Gateway struct{ api *telegram.Client }

var _ service.ReplySender = (*Gateway)(nil)

func NewGateway(api *telegram.Client) *Gateway { return &Gateway{api: api} }

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
