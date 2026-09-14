package domain

import "strings"

const (
	ProviderClaude     = "claude"
	ProviderGemini     = "gemini"
	ProviderGroq       = "groq"
	ProviderMistral    = "mistral"
	ProviderOpenRouter = "openrouter"
)

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

// Settings are runtime-tunable options (persisted, editable from the bot).
type Settings struct {
	ActiveProvider  string
	ClaudeModel     string
	GeminiModel     string
	GroqModel       string
	MistralModel    string
	OpenRouterModel string
	DebounceSeconds int
	Sensitivity     Sensitivity
	DigestEnabled   bool
	DigestTime      string // HH:MM in service timezone
	TriagePaused    bool
	MarkReadOnWork  bool
	// NotifyDoneOnClose: when closing a task with "✅ Закрыть", offer to send a "Готово!" message
	// to the contact. Opt-in; when on, the bot still asks for confirmation every time rather than
	// sending it automatically.
	NotifyDoneOnClose bool
}

// ModelFor returns configured model for provider.
func (s Settings) ModelFor(provider string) string {
	switch provider {
	case ProviderGemini:
		return s.GeminiModel
	case ProviderGroq:
		return s.GroqModel
	case ProviderMistral:
		return s.MistralModel
	case ProviderOpenRouter:
		return s.OpenRouterModel
	default:
		return s.ClaudeModel
	}
}

// ActiveModel returns model of the active provider.
func (s Settings) ActiveModel() string { return s.ModelFor(s.ActiveProvider) }
