// Package telegram is a minimal, dependency-free Telegram Bot API client
// with Telegram Business support (business connections and messages).
package telegram

import (
	"fmt"
	"strings"
)

// Update is an incoming update from getUpdates.
type Update struct {
	UpdateID                int64                    `json:"update_id"`
	Message                 *Message                 `json:"message,omitempty"`
	EditedMessage           *Message                 `json:"edited_message,omitempty"`
	CallbackQuery           *CallbackQuery           `json:"callback_query,omitempty"`
	BusinessConnection      *BusinessConnection      `json:"business_connection,omitempty"`
	BusinessMessage         *Message                 `json:"business_message,omitempty"`
	EditedBusinessMessage   *Message                 `json:"edited_business_message,omitempty"`
	DeletedBusinessMessages *BusinessMessagesDeleted `json:"deleted_business_messages,omitempty"`
}

// AllowedUpdates is the list of update types the service subscribes to.
var AllowedUpdates = []string{
	"message", "callback_query",
	"business_connection", "business_message", "edited_business_message", "deleted_business_messages",
}

type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

// FullName returns "First Last" or a fallback.
func (u *User) FullName() string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" && u.Username != "" {
		return "@" + u.Username
	}
	if name == "" {
		return fmt.Sprintf("id%d", u.ID)
	}
	return name
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type Media struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration,omitempty"`
	FileName string `json:"file_name,omitempty"`
}

type Sticker struct {
	FileID string `json:"file_id"`
	Emoji  string `json:"emoji,omitempty"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name,omitempty"`
}

type Poll struct {
	Question string `json:"question"`
}

type Message struct {
	MessageID            int       `json:"message_id"`
	From                 *User     `json:"from,omitempty"`
	SenderBusinessBot    *User     `json:"sender_business_bot,omitempty"`
	Chat                 Chat      `json:"chat"`
	Date                 int64     `json:"date"`
	BusinessConnectionID string    `json:"business_connection_id,omitempty"`
	ReplyToMessage       *Message  `json:"reply_to_message,omitempty"`
	Text                 string    `json:"text,omitempty"`
	Caption              string    `json:"caption,omitempty"`
	Photo                []Media   `json:"photo,omitempty"`
	Video                *Media    `json:"video,omitempty"`
	VideoNote            *Media    `json:"video_note,omitempty"`
	Voice                *Media    `json:"voice,omitempty"`
	Audio                *Media    `json:"audio,omitempty"`
	Animation            *Media    `json:"animation,omitempty"`
	Document             *Media    `json:"document,omitempty"`
	Sticker              *Sticker  `json:"sticker,omitempty"`
	Location             *Location `json:"location,omitempty"`
	Contact              *Contact  `json:"contact,omitempty"`
	Poll                 *Poll     `json:"poll,omitempty"`
}

// Content returns a textual representation of the message suitable for LLM analysis.
// Returns an empty string for service messages without meaningful content.
func (m *Message) Content() string {
	var parts []string
	switch {
	case len(m.Photo) > 0:
		parts = append(parts, "[фото]")
	case m.Video != nil:
		parts = append(parts, "[видео]")
	case m.VideoNote != nil:
		parts = append(parts, "[видеосообщение]")
	case m.Voice != nil:
		parts = append(parts, fmt.Sprintf("[голосовое сообщение %d c]", m.Voice.Duration))
	case m.Audio != nil:
		parts = append(parts, "[аудио]")
	case m.Animation != nil:
		parts = append(parts, "[GIF]")
	case m.Document != nil:
		parts = append(parts, fmt.Sprintf("[файл %s]", m.Document.FileName))
	case m.Sticker != nil:
		parts = append(parts, fmt.Sprintf("[стикер %s]", m.Sticker.Emoji))
	case m.Location != nil:
		parts = append(parts, fmt.Sprintf("[геопозиция %.5f,%.5f]", m.Location.Latitude, m.Location.Longitude))
	case m.Contact != nil:
		parts = append(parts, fmt.Sprintf("[контакт %s %s]", m.Contact.FirstName, m.Contact.PhoneNumber))
	case m.Poll != nil:
		parts = append(parts, fmt.Sprintf("[опрос: %s]", m.Poll.Question))
	}
	if m.Text != "" {
		parts = append(parts, m.Text)
	}
	if m.Caption != "" {
		parts = append(parts, m.Caption)
	}
	if m.ReplyToMessage != nil {
		quoted := m.ReplyToMessage.Text
		if quoted == "" {
			quoted = m.ReplyToMessage.Caption
		}
		if quoted != "" {
			r := []rune(quoted)
			if len(r) > 200 {
				quoted = string(r[:200]) + "…"
			}
			parts = append([]string{fmt.Sprintf("(в ответ на: «%s»)", quoted)}, parts...)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// BusinessBotRights describes what the bot may do on behalf of the business account.
type BusinessBotRights struct {
	CanReply              bool `json:"can_reply,omitempty"`
	CanReadMessages       bool `json:"can_read_messages,omitempty"`
	CanDeleteSentMessages bool `json:"can_delete_sent_messages,omitempty"`
}

type BusinessConnection struct {
	ID         string             `json:"id"`
	User       User               `json:"user"`
	UserChatID int64              `json:"user_chat_id"`
	Date       int64              `json:"date"`
	CanReply   bool               `json:"can_reply,omitempty"` // legacy field (Bot API < 9.0)
	Rights     *BusinessBotRights `json:"rights,omitempty"`
	IsEnabled  bool               `json:"is_enabled"`
}

// Replyable reports whether the bot may send messages via this connection.
func (c *BusinessConnection) Replyable() bool {
	if c.Rights != nil {
		return c.Rights.CanReply
	}
	return c.CanReply
}

// CanRead reports whether the bot may mark messages as read.
func (c *BusinessConnection) CanRead() bool {
	return c.Rights != nil && c.Rights.CanReadMessages
}

type BusinessMessagesDeleted struct {
	BusinessConnectionID string `json:"business_connection_id"`
	Chat                 Chat   `json:"chat"`
	MessageIDs           []int  `json:"message_ids"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string      `json:"text"`
	CallbackData string      `json:"callback_data,omitempty"`
	URL          string      `json:"url,omitempty"`
	WebApp       *WebAppInfo `json:"web_app,omitempty"`
}

// WebAppInfo points an inline button at a Telegram Mini App. Telegram requires URL to be https.
type WebAppInfo struct {
	URL string `json:"url"`
}

type LinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type SendMessageParams struct {
	BusinessConnectionID string                `json:"business_connection_id,omitempty"`
	ChatID               int64                 `json:"chat_id"`
	Text                 string                `json:"text"`
	ParseMode            string                `json:"parse_mode,omitempty"`
	ReplyMarkup          *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	LinkPreviewOptions   *LinkPreviewOptions   `json:"link_preview_options,omitempty"`
}

type EditMessageTextParams struct {
	ChatID             int64                 `json:"chat_id"`
	MessageID          int                   `json:"message_id"`
	Text               string                `json:"text"`
	ParseMode          string                `json:"parse_mode,omitempty"`
	ReplyMarkup        *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	LinkPreviewOptions *LinkPreviewOptions   `json:"link_preview_options,omitempty"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}
