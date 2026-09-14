package webapp

import (
	"fmt"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
)

// Task is the JSON representation of domain.Task for the Mini App.
type Task struct {
	ID             int64   `json:"id"`
	ChatID         int64   `json:"chat_id"`
	SenderName     string  `json:"sender_name"`
	SenderUsername string  `json:"sender_username"`
	ProfileURL     string  `json:"profile_url"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	SourceText     string  `json:"source_text"`
	Priority       string  `json:"priority"`
	Category       string  `json:"category"`
	Status         string  `json:"status"`
	Deadline       *string `json:"deadline"`
	Overdue        bool    `json:"overdue"`
	SnoozeUntil    *string `json:"snooze_until"`
	DraftReply     string  `json:"draft_reply"`
	ReplyStrategy  string  `json:"reply_strategy"`
	ReplySentAt    *string `json:"reply_sent_at"`
	ReplyText      string  `json:"reply_text"`
	Confidence     float64 `json:"confidence"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	ClosedAt       *string `json:"closed_at"`
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
		ID: t.ID, ChatID: t.ChatID,
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

// Settings is the JSON view of domain.Settings plus the choices available for each field.
type Settings struct {
	ActiveProvider    string   `json:"active_provider"`
	ClaudeModel       string   `json:"claude_model"`
	GeminiModel       string   `json:"gemini_model"`
	GroqModel         string   `json:"groq_model"`
	MistralModel      string   `json:"mistral_model"`
	OpenRouterModel   string   `json:"openrouter_model"`
	DebounceSeconds   int      `json:"debounce_seconds"`
	Sensitivity       string   `json:"sensitivity"`
	DigestEnabled     bool     `json:"digest_enabled"`
	DigestTime        string   `json:"digest_time"`
	TriagePaused      bool     `json:"triage_paused"`
	MarkReadOnWork    bool     `json:"mark_read_on_work"`
	NotifyDoneOnClose bool     `json:"notify_done_on_close"`
	Providers         []string `json:"providers"`
	ClaudePresets     []string `json:"claude_presets"`
	GeminiPresets     []string `json:"gemini_presets"`
	GroqPresets       []string `json:"groq_presets"`
	MistralPresets    []string `json:"mistral_presets"`
	OpenRouterPresets []string `json:"openrouter_presets"`
}

// settingsPatch is a partial update; nil fields are left untouched.
type settingsPatch struct {
	ActiveProvider    *string `json:"active_provider"`
	ClaudeModel       *string `json:"claude_model"`
	GeminiModel       *string `json:"gemini_model"`
	GroqModel         *string `json:"groq_model"`
	MistralModel      *string `json:"mistral_model"`
	OpenRouterModel   *string `json:"openrouter_model"`
	DebounceSeconds   *int    `json:"debounce_seconds"`
	Sensitivity       *string `json:"sensitivity"`
	DigestEnabled     *bool   `json:"digest_enabled"`
	DigestTime        *string `json:"digest_time"`
	TriagePaused      *bool   `json:"triage_paused"`
	MarkReadOnWork    *bool   `json:"mark_read_on_work"`
	NotifyDoneOnClose *bool   `json:"notify_done_on_close"`
}

func (p settingsPatch) apply(s *domain.Settings) {
	if p.ActiveProvider != nil {
		s.ActiveProvider = *p.ActiveProvider
	}
	if p.ClaudeModel != nil {
		s.ClaudeModel = *p.ClaudeModel
	}
	if p.GeminiModel != nil {
		s.GeminiModel = *p.GeminiModel
	}
	if p.GroqModel != nil {
		s.GroqModel = *p.GroqModel
	}
	if p.MistralModel != nil {
		s.MistralModel = *p.MistralModel
	}
	if p.OpenRouterModel != nil {
		s.OpenRouterModel = *p.OpenRouterModel
	}
	if p.DebounceSeconds != nil {
		s.DebounceSeconds = *p.DebounceSeconds
	}
	if p.Sensitivity != nil {
		s.Sensitivity = domain.Sensitivity(*p.Sensitivity)
	}
	if p.DigestEnabled != nil {
		s.DigestEnabled = *p.DigestEnabled
	}
	if p.DigestTime != nil {
		s.DigestTime = *p.DigestTime
	}
	if p.TriagePaused != nil {
		s.TriagePaused = *p.TriagePaused
	}
	if p.MarkReadOnWork != nil {
		s.MarkReadOnWork = *p.MarkReadOnWork
	}
	if p.NotifyDoneOnClose != nil {
		s.NotifyDoneOnClose = *p.NotifyDoneOnClose
	}
}
