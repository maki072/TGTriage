package webapp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
)

// Me describes the Mini App user.
type Me struct {
	UserID          int64  `json:"user_id"`
	Name            string `json:"name"`
	Role            string `json:"role"`     // owner | operator
	Helpdesk        bool   `json:"helpdesk"` // enabled and configured
	HelpdeskEnabled bool   `json:"helpdesk_enabled"`
}

// Task is the JSON representation of domain.Task for the Mini App.
type Task struct {
	ID             int64         `json:"id"`
	ChatID         int64         `json:"chat_id"`
	Forwarded      bool          `json:"forwarded"` // created from a forwarded message: no chat to reply to
	Helpdesk       bool          `json:"helpdesk"`
	BotID          int64         `json:"bot_id"` // 0 = main bot; which bot's desk this ticket belongs to
	HelpdeskUser   *HelpdeskUser `json:"helpdesk_user,omitempty"`
	SenderName     string        `json:"sender_name"`
	SenderUsername string        `json:"sender_username"`
	ProfileURL     string        `json:"profile_url"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	SourceText     string        `json:"source_text"`
	Priority       string        `json:"priority"`
	Category       string        `json:"category"`
	Status         string        `json:"status"`
	Deadline       *string       `json:"deadline"`
	Overdue        bool          `json:"overdue"`
	SnoozeUntil    *string       `json:"snooze_until"`
	DraftReply     string        `json:"draft_reply"`
	ReplyStrategy  string        `json:"reply_strategy"`
	ReplySentAt    *string       `json:"reply_sent_at"`
	ReplyText      string        `json:"reply_text"`
	Confidence     float64       `json:"confidence"`
	Provider       string        `json:"provider"`
	Model          string        `json:"model"`
	Importance     string        `json:"importance"`
	RemindAt       *string       `json:"remind_at"`
	MergedInto     int64         `json:"merged_into,omitempty"`
	CreatedAt      string        `json:"created_at"`
	UpdatedAt      string        `json:"updated_at"`
	ClosedAt       *string       `json:"closed_at"`
}

func fmtTime(t time.Time, loc *time.Location) string { return t.In(loc).Format(time.RFC3339) }

func fmtTimePtr(t *time.Time, loc *time.Location) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := fmtTime(*t, loc)
	return &s
}

func profileURL(username string, userID int64) string {
	if username != "" {
		return "https://t.me/" + username
	}
	return fmt.Sprintf("tg://user?id=%d", userID)
}

func toTaskDTO(t domain.Task, loc *time.Location) Task {
	now := time.Now()
	return Task{
		ID: t.ID, ChatID: t.ChatID, Forwarded: !t.HasChat(), Helpdesk: t.IsHelpdesk(),
		BotID:      domain.ParseHelpdeskBotID(t.ConnectionID),
		SenderName: t.SenderName, SenderUsername: t.SenderUsername,
		ProfileURL:    profileURL(t.SenderUsername, t.SenderID),
		Title:         t.Title,
		Description:   t.Description,
		SourceText:    t.SourceText,
		Priority:      string(t.Priority),
		Category:      string(t.Category),
		Status:        string(t.Status),
		Deadline:      fmtTimePtr(t.Deadline, loc),
		Overdue:       t.IsOverdue(now),
		SnoozeUntil:   fmtTimePtr(t.SnoozeUntil, loc),
		DraftReply:    t.DraftReply,
		ReplyStrategy: t.ReplyStrategy,
		ReplySentAt:   fmtTimePtr(t.ReplySentAt, loc),
		ReplyText:     t.ReplyText,
		Confidence:    t.Confidence,
		Provider:      t.Provider,
		Model:         t.Model,
		Importance:    string(t.Importance),
		RemindAt:      fmtTimePtr(t.RemindAt, loc),
		MergedInto:    t.MergedInto,
		CreatedAt:     fmtTime(t.CreatedAt, loc),
		UpdatedAt:     fmtTime(t.UpdatedAt, loc),
		ClosedAt:      fmtTimePtr(t.ClosedAt, loc),
	}
}

func toTaskDTOs(ts []domain.Task, loc *time.Location) []Task {
	out := make([]Task, len(ts))
	for i, t := range ts {
		out[i] = toTaskDTO(t, loc)
	}
	return out
}

// TaskList is a paginated task list response.
type TaskList struct {
	Items  []Task `json:"items"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// HelpdeskUser is the JSON view of a support desk user.
type HelpdeskUser struct {
	UserID        int64   `json:"user_id"`
	BotID         int64   `json:"bot_id"` // 0 = main bot; which bot this contact wrote to
	Name          string  `json:"name"`
	Username      string  `json:"username"`
	LanguageCode  string  `json:"language_code"`
	Source        string  `json:"source"`
	ProfileURL    string  `json:"profile_url"`
	TopicURL      string  `json:"topic_url"`
	TopicClosed   bool    `json:"topic_closed"`
	Blocked       bool    `json:"blocked"`
	Banned        bool    `json:"banned"`
	BannedAt      *string `json:"banned_at"`
	AwaitingSince *string `json:"awaiting_since"`
	LastMessageAt *string `json:"last_message_at"`
	CreatedAt     string  `json:"created_at"`
}

func toHDUserDTO(u *domain.HelpdeskUser, topicURL string, loc *time.Location) HelpdeskUser {
	return HelpdeskUser{
		UserID: u.UserID, BotID: u.BotID, Name: u.Name, Username: u.Username, LanguageCode: u.LanguageCode, Source: u.Source,
		ProfileURL: profileURL(u.Username, u.UserID), TopicURL: topicURL, TopicClosed: u.TopicClosed, Blocked: u.Blocked, Banned: u.Banned, BannedAt: fmtTimePtr(u.BannedAt, loc),
		AwaitingSince: fmtTimePtr(u.AwaitingSince, loc), LastMessageAt: fmtTimePtr(u.LastMessageAt, loc),
		CreatedAt: fmtTime(u.CreatedAt, loc),
	}
}

// DialogMessage is one message of a helpdesk conversation.
type DialogMessage struct {
	ID       int64  `json:"id"`
	Outgoing bool   `json:"outgoing"`
	Text     string `json:"text"`
	SentAt   string `json:"sent_at"`
}

func toDialogMessages(msgs []domain.Message, loc *time.Location) []DialogMessage {
	out := make([]DialogMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, DialogMessage{ID: m.ID, Outgoing: m.Outgoing, Text: m.Text, SentAt: fmtTime(m.SentAt, loc)})
	}
	return out
}

// HelpdeskDialog is a user with the conversation and tickets.
type HelpdeskDialog struct {
	User     HelpdeskUser    `json:"user"`
	Messages []DialogMessage `json:"messages"`
	Tickets  []Task          `json:"tickets"`
}

// Connection is the JSON view of the owner's Business connection.
type Connection struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	CanReply bool   `json:"can_reply"`
	CanRead  bool   `json:"can_read"`
}

// Overview mirrors the bot's main-menu summary.
type Overview struct {
	New        int         `json:"new"`
	InProgress int         `json:"in_progress"`
	Snoozed    int         `json:"snoozed"`
	Overdue    int         `json:"overdue"`
	Awaiting   int         `json:"awaiting"` // helpdesk users waiting for an answer
	Connection *Connection `json:"connection"`
}

// Digest mirrors service.Digest.
type Digest struct {
	Date       string `json:"date"`
	New        int    `json:"new"`
	InProgress int    `json:"in_progress"`
	Snoozed    int    `json:"snoozed"`
	Overdue    []Task `json:"overdue"`
	DueSoon    []Task `json:"due_soon"`
	Stale      []Task `json:"stale"`
}

func toDigestDTO(d *service.Digest, loc *time.Location) Digest {
	return Digest{
		Date: d.Date.Format("2006-01-02"), New: d.New, InProgress: d.InProgress, Snoozed: d.Snoozed,
		Overdue: toTaskDTOs(d.Overdue, loc), DueSoon: toTaskDTOs(d.DueSoon, loc), Stale: toTaskDTOs(d.Stale, loc),
	}
}

// Stats mirrors service.Stats.
type Stats struct {
	Days     int            `json:"days"`
	Tasks    map[string]int `json:"tasks"`
	Analyses AnalysisStats  `json:"analyses"`
}

type AnalysisStats struct {
	Total        int            `json:"total"`
	Errors       int            `json:"errors"`
	Noise        int            `json:"noise"`
	WithTask     int            `json:"with_task"`
	AvgLatencyMs float64        `json:"avg_latency_ms"`
	ByProvider   map[string]int `json:"by_provider"`
}

func toStatsDTO(s *service.Stats) Stats {
	tasks := make(map[string]int, len(s.Tasks))
	for k, v := range s.Tasks {
		tasks[string(k)] = v
	}
	by := s.Analyses.ByProvider
	if by == nil {
		by = map[string]int{}
	}
	return Stats{
		Days: s.Days, Tasks: tasks,
		Analyses: AnalysisStats{
			Total: s.Analyses.Total, Errors: s.Analyses.Errors, Noise: s.Analyses.Noise,
			WithTask: s.Analyses.WithTask, AvgLatencyMs: s.Analyses.AvgLatencyMs, ByProvider: by,
		},
	}
}

// Bot is the JSON view of an additional bot for the "Боты" settings section. The token itself
// never leaves the server after it's been saved.
type Bot struct {
	ID          int64       `json:"id"`
	Label       string      `json:"label"`
	Username    string      `json:"username"`
	Active      bool        `json:"active"`
	Sensitivity string      `json:"sensitivity"` // "" = inherit the shared setting
	AIChainLen  int         `json:"ai_chain_len"`
	Helpdesk    BotHelpdesk `json:"helpdesk"`
}

// BotHelpdesk is an additional bot's own, independent support desk configuration.
type BotHelpdesk struct {
	Enabled          bool   `json:"enabled"`
	GroupID          int64  `json:"group_id"`
	TriageEnabled    bool   `json:"triage_enabled"`
	About            string `json:"about"`
	GreetingEnabled  bool   `json:"greeting_enabled"`
	GreetingText     string `json:"greeting_text"`
	AutoReplyEnabled bool   `json:"autoreply_enabled"`
	AutoReplyText    string `json:"autoreply_text"`
	HoursEnabled     bool   `json:"hours_enabled"`
	HoursStart       string `json:"hours_start"`
	HoursEnd         string `json:"hours_end"`
	HoursDays        string `json:"hours_days"`
	OffHoursText     string `json:"offhours_text"`
	ReminderMinutes  int    `json:"reminder_minutes"`
}

func toBotDTO(b domain.Bot) Bot {
	h := b.Helpdesk
	return Bot{
		ID: b.ID, Label: b.Label, Username: b.Username, Active: b.Active,
		Sensitivity: string(b.Sensitivity), AIChainLen: len(b.AIChain),
		Helpdesk: BotHelpdesk{
			Enabled: h.Enabled, GroupID: h.GroupID, TriageEnabled: h.TriageEnabled, About: h.About,
			GreetingEnabled: h.GreetingEnabled, GreetingText: h.GreetingText,
			AutoReplyEnabled: h.AutoReplyEnabled, AutoReplyText: h.AutoReplyText,
			HoursEnabled: h.HoursEnabled, HoursStart: h.HoursStart, HoursEnd: h.HoursEnd, HoursDays: h.HoursDays,
			OffHoursText: h.OffHoursText, ReminderMinutes: h.ReminderMinutes,
		},
	}
}

func toBotDTOs(bots []domain.Bot) []Bot {
	out := make([]Bot, len(bots))
	for i, b := range bots {
		out[i] = toBotDTO(b)
	}
	return out
}

// AIKey is the JSON view of an AI chain entry. The key itself never leaves the server.
type AIKey struct {
	Provider  string `json:"provider"`
	KeyID     string `json:"key_id"`
	KeyMasked string `json:"key_masked"`
}

// keyID identifies a stored API key to the Mini App without revealing it: the frontend echoes it
// back for rows the owner didn't retype, and applyChain swaps it for the real key.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

func toAIChainDTO(chain []domain.AIKey) []AIKey {
	out := make([]AIKey, len(chain))
	for i, k := range chain {
		out[i] = AIKey{Provider: k.Provider, KeyID: keyID(k.Key), KeyMasked: domain.MaskKey(k.Key)}
	}
	return out
}

// SettingValue is a setting description with its current value.
type SettingValue struct {
	service.SettingField
	Value any `json:"value"`
}

// Settings is the settings screen: field schema with values plus the AI chain.
type Settings struct {
	Groups    []service.SettingGroup `json:"groups"`
	Fields    []SettingValue         `json:"fields"`
	AIChain   []AIKey                `json:"ai_chain"`
	Providers []string               `json:"providers"`
}

// aiKeyPatch is one row of a saved AI chain: either a newly typed Key, or the KeyID of a key the
// server already stores (see keyID).
type aiKeyPatch struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	KeyID    string `json:"key_id"`
}

func applyChain(s *domain.Settings, rows []aiKeyPatch) {
	chain := make([]domain.AIKey, 0, len(rows))
	for _, e := range rows {
		key := strings.TrimSpace(e.Key)
		if key == "" && e.KeyID != "" {
			for _, old := range s.AIChain {
				if keyID(old.Key) == e.KeyID {
					key = old.Key
					break
				}
			}
		}
		// An unresolved row keeps an empty key and is rejected by settings validation.
		chain = append(chain, domain.AIKey{Provider: e.Provider, Key: key})
	}
	s.AIChain = chain
}
