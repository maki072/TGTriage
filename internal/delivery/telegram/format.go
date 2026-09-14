package tgbot

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
	"tgtriage/internal/telegram"
)

func esc(s string) string { return html.EscapeString(s) }

func trunc(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

func priorityEmoji(p domain.Priority) string {
	switch p {
	case domain.PriorityCritical:
		return "🔴"
	case domain.PriorityHigh:
		return "🟠"
	case domain.PriorityMedium:
		return "🟡"
	default:
		return "🟢"
	}
}

func priorityName(p domain.Priority) string {
	switch p {
	case domain.PriorityCritical:
		return "критический"
	case domain.PriorityHigh:
		return "высокий"
	case domain.PriorityMedium:
		return "средний"
	default:
		return "низкий"
	}
}

func statusLabel(s domain.TaskStatus) string {
	switch s {
	case domain.StatusNew:
		return "🆕 Новая"
	case domain.StatusInProgress:
		return "👀 В работе"
	case domain.StatusSnoozed:
		return "⏰ Отложена"
	case domain.StatusDone:
		return "✅ Завершена"
	case domain.StatusFalsePositive:
		return "🗑 Ошибка триажа"
	default:
		return string(s)
	}
}

func categoryLabel(c domain.Category) string {
	switch c {
	case domain.CategoryBug:
		return "🐞 Баг"
	case domain.CategoryHelp:
		return "🆘 Помощь"
	case domain.CategoryTask:
		return "📌 Задача"
	case domain.CategoryQuestion:
		return "❓ Вопрос"
	case domain.CategoryDeadline:
		return "⏳ Дедлайн"
	case domain.CategoryAgreement:
		return "🤝 Договорённость"
	default:
		return "📎 Другое"
	}
}

func strategyLabel(s string) string {
	switch s {
	case "confirm":
		return "подтверждение"
	case "clarify":
		return "уточняющий вопрос"
	case "decline":
		return "отказ"
	default:
		return "ответ"
	}
}

func providerTitle(p string) string {
	switch p {
	case domain.ProviderClaude:
		return "Claude"
	case domain.ProviderGemini:
		return "Gemini"
	case domain.ProviderGroq:
		return "Groq"
	case domain.ProviderMistral:
		return "Mistral"
	case domain.ProviderOpenRouter:
		return "OpenRouter"
	default:
		return p
	}
}

func sensitivityName(s domain.Sensitivity) string {
	switch s {
	case domain.SensitivityLow:
		return "низкая"
	case domain.SensitivityHigh:
		return "высокая"
	default:
		return "средняя"
	}
}

func yesNo(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}

func (b *Bot) fmtTime(t time.Time) string  { return t.In(b.cfg.Location).Format("02.01.2006 15:04") }
func (b *Bot) fmtShort(t time.Time) string { return t.In(b.cfg.Location).Format("02.01 15:04") }

func profileURL(username string, userID int64) string {
	if username != "" {
		return "https://t.me/" + username
	}
	return fmt.Sprintf("tg://user?id=%d", userID)
}

func messageURL(userID int64, messageID int) string {
	return fmt.Sprintf("tg://openmessage?user_id=%d&message_id=%d", userID, messageID)
}

func humanError(err error) string {
	var (
		apiErr *telegram.APIError
		aiErr  *ai.Error
	)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "Объект не найден"
	case errors.Is(err, domain.ErrNoConnection):
		return "Бот не подключён к Telegram Business (Настройки Telegram → Telegram Business → Чат-боты)"
	case errors.Is(err, domain.ErrCannotReply):
		return "У бота нет права отвечать на сообщения — включите его в настройках чат-бота Telegram Business"
	case errors.Is(err, domain.ErrNoSourceChat):
		return "Задача создана из пересланного сообщения — ответить собеседнику из бота нельзя"
	case errors.Is(err, domain.ErrEmptyReply):
		return "Текст ответа пуст"
	case errors.Is(err, domain.ErrProviderUnset):
		return "AI-провайдер не настроен"
	case errors.As(err, &apiErr):
		return "Telegram: " + apiErr.Description
	case errors.As(err, &aiErr):
		return aiErr.Error()
	case errors.Is(err, domain.ErrInvalidInput):
		return strings.TrimPrefix(err.Error(), domain.ErrInvalidInput.Error()+": ")
	default:
		return trunc(err.Error(), 300)
	}
}

type button = telegram.InlineKeyboardButton

func cb(text, data string) button { return button{Text: text, CallbackData: data} }

func webAppButton(text, url string) button {
	return button{Text: text, WebApp: &telegram.WebAppInfo{URL: url}}
}

func row(btns ...button) []button { return btns }

func kb(rows ...[]button) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func mark(selected bool, label string) string {
	if selected {
		return "• " + label + " •"
	}
	return label
}
