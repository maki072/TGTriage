// Package service contains application use cases: triage pipeline, task management,
// runtime settings and background scheduling.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"tgtriage/internal/domain"
)

const metaPrefix = "meta."

const (
	keyChain = "ai.chain" // JSON array of domain.AIKey
	// keyLegacyProvider is the pre-chain "active provider" override. Still honored on load — that
	// provider's keys move to the front of the .env chain — until the chain itself is saved.
	keyLegacyProvider  = "ai.provider"
	keyClaudeModel     = "ai.claude_model"
	keyGeminiModel     = "ai.gemini_model"
	keyGroqModel       = "ai.groq_model"
	keyMistralModel    = "ai.mistral_model"
	keyOpenRouterModel = "ai.openrouter_model"
	keyDebounce        = "triage.debounce_seconds"
	keySensitivity     = "triage.sensitivity"
	keyPaused          = "triage.paused"
	keyMarkRead        = "triage.mark_read_on_work"
	keyDigestOn        = "digest.enabled"
	keyDigestTime      = "digest.time"
	keyNotifyDone      = "task.notify_done_on_close"
)

// maxChainLen bounds the AI chain: every entry may cost a full retry budget on a bad day.
const maxChainLen = 20

var modelNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$`)

// ValidModelName reports whether s looks like a provider model identifier.
func ValidModelName(s string) bool { return modelNameRe.MatchString(s) }

// ParseClock parses "HH:MM".
func ParseClock(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%w: expected HH:MM", domain.ErrInvalidInput)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("%w: expected HH:MM", domain.ErrInvalidInput)
	}
	return h, m, nil
}

// SettingsService keeps runtime settings in memory, persisted in the DB.
// Defaults come from environment; values changed via the bot override them.
type SettingsService struct {
	repo     domain.SettingsRepository
	defaults domain.Settings

	mu      sync.RWMutex
	current domain.Settings
}

func NewSettingsService(ctx context.Context, repo domain.SettingsRepository, defaults domain.Settings) (*SettingsService, error) {
	s := &SettingsService{repo: repo, defaults: defaults}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SettingsService) reload(ctx context.Context) error {
	values, err := s.repo.All(ctx)
	if err != nil {
		return err
	}
	cur := s.defaults
	cur.AIChain = slices.Clone(s.defaults.AIChain)
	if v := values[keyChain]; v != "" {
		var chain []domain.AIKey
		if err := json.Unmarshal([]byte(v), &chain); err == nil {
			cur.AIChain = chain
		}
	} else if p := values[keyLegacyProvider]; p != "" {
		cur.AIChain = promote(cur.AIChain, p)
	}
	if v := values[keyClaudeModel]; v != "" {
		cur.ClaudeModel = v
	}
	if v := values[keyGeminiModel]; v != "" {
		cur.GeminiModel = v
	}
	if v := values[keyGroqModel]; v != "" {
		cur.GroqModel = v
	}
	if v := values[keyMistralModel]; v != "" {
		cur.MistralModel = v
	}
	if v := values[keyOpenRouterModel]; v != "" {
		cur.OpenRouterModel = v
	}
	if n, err := strconv.Atoi(values[keyDebounce]); err == nil && n > 0 {
		cur.DebounceSeconds = n
	}
	if v, ok := domain.ParseSensitivity(values[keySensitivity]); ok {
		cur.Sensitivity = v
	}
	if b, err := strconv.ParseBool(values[keyPaused]); err == nil {
		cur.TriagePaused = b
	}
	if b, err := strconv.ParseBool(values[keyMarkRead]); err == nil {
		cur.MarkReadOnWork = b
	}
	if b, err := strconv.ParseBool(values[keyDigestOn]); err == nil {
		cur.DigestEnabled = b
	}
	if v := values[keyDigestTime]; v != "" {
		if _, _, err := ParseClock(v); err == nil {
			cur.DigestTime = v
		}
	}
	if b, err := strconv.ParseBool(values[keyNotifyDone]); err == nil {
		cur.NotifyDoneOnClose = b
	}
	s.mu.Lock()
	s.current = cur
	s.mu.Unlock()
	return nil
}

// promote moves provider's entries to the front of chain, keeping relative order otherwise.
func promote(chain []domain.AIKey, provider string) []domain.AIKey {
	out := make([]domain.AIKey, 0, len(chain))
	for _, k := range chain {
		if k.Provider == provider {
			out = append(out, k)
		}
	}
	for _, k := range chain {
		if k.Provider != provider {
			out = append(out, k)
		}
	}
	return out
}

// Get returns a snapshot of current settings.
func (s *SettingsService) Get() domain.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// encodeSettings serializes every persisted field to its storage representation.
func encodeSettings(st domain.Settings) map[string]string {
	chain, _ := json.Marshal(st.AIChain) // a slice of plain string structs always marshals
	return map[string]string{
		keyChain:           string(chain),
		keyClaudeModel:     st.ClaudeModel,
		keyGeminiModel:     st.GeminiModel,
		keyGroqModel:       st.GroqModel,
		keyMistralModel:    st.MistralModel,
		keyOpenRouterModel: st.OpenRouterModel,
		keyDebounce:        strconv.Itoa(st.DebounceSeconds),
		keySensitivity:     string(st.Sensitivity),
		keyPaused:          strconv.FormatBool(st.TriagePaused),
		keyMarkRead:        strconv.FormatBool(st.MarkReadOnWork),
		keyDigestOn:        strconv.FormatBool(st.DigestEnabled),
		keyDigestTime:      st.DigestTime,
		keyNotifyDone:      strconv.FormatBool(st.NotifyDoneOnClose),
	}
}

// Update applies fn atomically, validates and persists only the fields that actually changed.
// A field nobody has touched is left absent from the DB, so it keeps following the .env default
// (and a later env change or "reset to .env" takes effect for it) until this same field is edited again.
func (s *SettingsService) Update(ctx context.Context, fn func(*domain.Settings)) (domain.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.current
	next.AIChain = slices.Clone(s.current.AIChain)
	fn(&next)
	next.AIChain = slices.Clone(next.AIChain) // fn may have put in a slice the caller still holds
	for i := range next.AIChain {
		next.AIChain[i].Provider = strings.ToLower(strings.TrimSpace(next.AIChain[i].Provider))
		next.AIChain[i].Key = strings.TrimSpace(next.AIChain[i].Key)
	}
	if err := s.validate(next); err != nil {
		return s.current, err
	}
	before, after := encodeSettings(s.current), encodeSettings(next)
	changed := make(map[string]string, len(after))
	for k, v := range after {
		if before[k] != v {
			changed[k] = v
		}
	}
	if len(changed) > 0 {
		if err := s.repo.SetMany(ctx, changed); err != nil {
			return s.current, err
		}
	}
	s.current = next
	return next, nil
}

func (s *SettingsService) validate(st domain.Settings) error {
	// An empty chain is allowed (triage then reports that no AI is configured): rejecting it would
	// also block every unrelated setting change on a fresh install without keys.
	if len(st.AIChain) > maxChainLen {
		return fmt.Errorf("%w: не больше %d ключей AI", domain.ErrInvalidInput, maxChainLen)
	}
	for i, k := range st.AIChain {
		switch {
		case !domain.KnownProvider(k.Provider):
			return fmt.Errorf("%w: строка %d: неизвестный провайдер %q", domain.ErrInvalidInput, i+1, k.Provider)
		case k.Key == "":
			return fmt.Errorf("%w: строка %d: пустой API-ключ", domain.ErrInvalidInput, i+1)
		case len(k.Key) > 512 || strings.ContainsFunc(k.Key, unicode.IsSpace):
			return fmt.Errorf("%w: строка %d: некорректный API-ключ", domain.ErrInvalidInput, i+1)
		}
	}
	if !ValidModelName(st.ClaudeModel) || !ValidModelName(st.GeminiModel) || !ValidModelName(st.GroqModel) ||
		!ValidModelName(st.MistralModel) || !ValidModelName(st.OpenRouterModel) {
		return fmt.Errorf("%w: invalid model name", domain.ErrInvalidInput)
	}
	if st.DebounceSeconds < 1 || st.DebounceSeconds > 600 {
		return fmt.Errorf("%w: debounce must be within 1..600 seconds", domain.ErrInvalidInput)
	}
	if _, ok := domain.ParseSensitivity(string(st.Sensitivity)); !ok {
		return fmt.Errorf("%w: invalid sensitivity", domain.ErrInvalidInput)
	}
	if _, _, err := ParseClock(st.DigestTime); err != nil {
		return err
	}
	return nil
}

// Reset drops all overrides and returns to environment defaults.
func (s *SettingsService) Reset(ctx context.Context) error {
	if err := s.repo.DeleteExceptPrefix(ctx, metaPrefix); err != nil {
		return err
	}
	return s.reload(ctx)
}

// Meta reads an internal service value (not a user setting).
func (s *SettingsService) Meta(ctx context.Context, key string) (string, error) {
	v, _, err := s.repo.Get(ctx, metaPrefix+key)
	return v, err
}

// SetMeta writes an internal service value.
func (s *SettingsService) SetMeta(ctx context.Context, key, value string) error {
	return s.repo.Set(ctx, metaPrefix+key, value)
}
