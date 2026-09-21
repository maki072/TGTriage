package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	ProviderClaude     = "claude"
	ProviderGemini     = "gemini"
	ProviderGroq       = "groq"
	ProviderMistral    = "mistral"
	ProviderOpenRouter = "openrouter"
)

// Providers lists every supported LLM provider in display order.
var Providers = []string{ProviderClaude, ProviderGemini, ProviderGroq, ProviderMistral, ProviderOpenRouter}

// KnownProvider reports whether p is a supported provider name.
func KnownProvider(p string) bool {
	for _, v := range Providers {
		if v == p {
			return true
		}
	}
	return false
}

// AIKey is one entry of the AI fallback chain: a provider and an API key for it.
type AIKey struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// MaskKey hides an API key for display, keeping just enough to tell keys apart.
func MaskKey(key string) string {
	if len(key) <= 12 {
		return "…"
	}
	return key[:4] + "…" + key[len(key)-4:]
}

// Sensitivity controls how aggressively messages are classified as tasks.
type Sensitivity string

const (
	SensitivityLow    Sensitivity = "low"
	SensitivityMedium Sensitivity = "medium"
	SensitivityHigh   Sensitivity = "high"
)

func ParseSensitivity(s string) (Sensitivity, bool) {
	switch v := Sensitivity(strings.ToLower(strings.TrimSpace(s))); v {
	case SensitivityLow, SensitivityMedium, SensitivityHigh:
		return v, true
	}
	return SensitivityMedium, false
}

// Threshold is the minimal LLM confidence required to create a task.
func (s Sensitivity) Threshold() float64 {
	switch s {
	case SensitivityLow:
		return 0.75
	case SensitivityHigh:
		return 0.35
	default:
		return 0.55
	}
}

// ProviderSettings configures one LLM adapter.
type ProviderSettings struct {
	Model     string
	Presets   []string
	MaxTokens int
	BaseURL   string
}

// Settings are runtime-tunable options, persisted in the DB and editable from the Mini App (and partly
// from the bot). Only bootstrap values (bot token, owner, DB path, listen address, proxy) live in env.
type Settings struct {
	// AIChain is tried top to bottom for every LLM call: when an entry fails, the next one is used.
	AIChain         []AIKey
	Claude          ProviderSettings
	Gemini          ProviderSettings
	Groq            ProviderSettings
	Mistral         ProviderSettings
	OpenRouter      ProviderSettings
	ClaudeEffort    string
	ClaudeFallbacks bool
	AITimeoutSec    int
	AIMaxRetries    int
	AnalysisWorkers int // applied on restart

	DebounceSeconds        int
	DebounceMaxWaitSeconds int
	ContextMessages        int
	NoisePrefilter         bool
	Sensitivity            Sensitivity
	TriagePaused           bool
	MarkReadOnWork         bool
	// AutoCloseOnDone: a short "готово" / "сделал" written by the owner in a Business chat closes the open task of that chat.
	AutoCloseOnDone bool
	// NotifyDoneOnClose: when closing a task with "✅ Закрыть", offer to send a "Готово" message
	// to the contact. Opt-in; when on, the bot still asks for confirmation every time rather than
	// sending it automatically.
	NotifyDoneOnClose bool
	OwnerAbout        string
	DeepLinks         bool
	// PersonalReminderMinutes: how often to re-notify the owner about an open task that came from a
	// personal (Telegram Business) chat, until it's closed. 0 — no repeated reminders.
	PersonalReminderMinutes int

	DigestEnabled bool
	DigestTime    string // HH:MM in service timezone
	Timezone      string // IANA name

	WebAppPublicURL string
	RetentionDays   int

	Helpdesk HelpdeskSettings
	Backup   BackupSettings
}

// HelpdeskSettings configure the support desk: users write to the bot, operators answer in forum topics.
type HelpdeskSettings struct {
	Enabled          bool
	GroupID          int64 // forum supergroup with operators
	TriageEnabled    bool
	About            string // what the support desk is about, for the LLM
	GreetingEnabled  bool
	GreetingText     string
	AutoReplyEnabled bool
	AutoReplyText    string
	HoursEnabled     bool
	HoursStart       string // HH:MM
	HoursEnd         string // HH:MM
	HoursDays        string // ISO weekday digits, "12345" = Mon..Fri
	OffHoursText     string // {hours} is replaced with HoursLabel()
	ReminderMinutes  int    // 0 = no reminders about unanswered users
	// SpamScreen holds the messages of new users that look like spam (links, @mentions, forwards,
	// Chinese/Arabic/... text) in a quarantine topic until an operator approves or bans them.
	SpamScreen bool
	// SpamCaptcha makes every new user press "I am not a bot" before their first message is relayed.
	SpamCaptcha bool
}

// DefaultHelpdeskSettings mirrors the built-in defaults of the "helpdesk.*" setting fields
// (internal/service/settings_fields.go) — used to seed a newly added bot's own support desk.
func DefaultHelpdeskSettings() HelpdeskSettings {
	return HelpdeskSettings{
		Enabled:         true,
		TriageEnabled:   true,
		GreetingEnabled: true,
		GreetingText:    "Здравствуйте! Напишите ваш вопрос одним или несколькими сообщениями — мы ответим прямо здесь.",
		AutoReplyText:   "Спасибо, сообщение получено. Оператор ответит в ближайшее время.",
		HoursStart:      "09:00",
		HoursEnd:        "18:00",
		HoursDays:       "12345",
		OffHoursText:    "Сейчас нерабочее время. Сообщение получено, ответим в рабочие часы: {hours}.",
		ReminderMinutes: 15,
		SpamScreen:      true,
	}
}

// Active reports whether the desk is switched on and has a group to work in.
func (h HelpdeskSettings) Active() bool { return h.Enabled && h.GroupID != 0 }

// InWorkingHours reports whether t (already in the service timezone) falls into working hours.
// With working hours switched off every moment is a working one.
func (h HelpdeskSettings) InWorkingHours(t time.Time) bool {
	if !h.HoursEnabled {
		return true
	}
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	start, okS := clockMinutes(h.HoursStart)
	end, okE := clockMinutes(h.HoursEnd)
	if !okS || !okE {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	today := strings.ContainsRune(h.HoursDays, rune('0'+wd))
	if start <= end {
		return today && m >= start && m < end
	}
	// overnight shift, e.g. 22:00–06:00: the early-morning part belongs to the previous day's shift
	prev := wd - 1
	if prev == 0 {
		prev = 7
	}
	return today && m >= start || strings.ContainsRune(h.HoursDays, rune('0'+prev)) && m < end
}

// HoursLabel renders working hours for users, e.g. "пн–пт 09:00–18:00".
func (h HelpdeskSettings) HoursLabel() string {
	names := [...]string{"", "пн", "вт", "ср", "чт", "пт", "сб", "вс"}
	var parts []string
	for d := 1; d <= 7; d++ {
		if !strings.ContainsRune(h.HoursDays, rune('0'+d)) {
			continue
		}
		end := d
		for end+1 <= 7 && strings.ContainsRune(h.HoursDays, rune('0'+end+1)) {
			end++
		}
		switch {
		case end == d:
			parts = append(parts, names[d])
		case end == d+1:
			parts = append(parts, names[d], names[end])
		default:
			parts = append(parts, names[d]+"–"+names[end])
		}
		d = end
	}
	return strings.TrimSpace(strings.Join(parts, ", ") + " " + h.HoursStart + "–" + h.HoursEnd)
}

func clockMinutes(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// BackupSettings configure scheduled database backups.
type BackupSettings struct {
	Enabled      bool
	Time         string // HH:MM in service timezone
	Keep         int    // how many backup files stay on disk
	SendTelegram bool   // send every backup to the owner's chat with the bot
}

// Provider returns the settings of the given provider (Claude for unknown names).
func (s *Settings) Provider(name string) *ProviderSettings {
	switch name {
	case ProviderGemini:
		return &s.Gemini
	case ProviderGroq:
		return &s.Groq
	case ProviderMistral:
		return &s.Mistral
	case ProviderOpenRouter:
		return &s.OpenRouter
	default:
		return &s.Claude
	}
}

// ModelFor returns configured model for provider.
func (s Settings) ModelFor(provider string) string { return s.Provider(provider).Model }

// Primary returns the first AI chain entry — the one used while it works.
func (s Settings) Primary() (AIKey, bool) {
	if len(s.AIChain) == 0 {
		return AIKey{}, false
	}
	return s.AIChain[0], true
}

// ChainProviders returns the distinct providers of the AI chain in chain order.
func (s Settings) ChainProviders() []string {
	var out []string
	for _, k := range s.AIChain {
		dup := false
		for _, p := range out {
			dup = dup || p == k.Provider
		}
		if !dup {
			out = append(out, k.Provider)
		}
	}
	return out
}

// Location resolves the configured timezone, falling back to UTC.
func (s Settings) Location() *time.Location {
	if loc, err := time.LoadLocation(s.Timezone); err == nil && s.Timezone != "" {
		return loc
	}
	return time.UTC
}

// TopicLink returns a t.me link to a forum topic of a supergroup (-100… chat id).
func TopicLink(groupID int64, topicID int) string {
	internal := strings.TrimPrefix(fmt.Sprint(groupID), "-100")
	if topicID == 0 {
		return "https://t.me/c/" + internal
	}
	return fmt.Sprintf("https://t.me/c/%s/%d", internal, topicID)
}
