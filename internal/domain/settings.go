package domain

import "strings"

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

// Settings are runtime-tunable options (persisted, editable from the bot).
type Settings struct {
	// AIChain is tried top to bottom for every LLM call: when an entry fails, the next one is used.
	AIChain         []AIKey
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
