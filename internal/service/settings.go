// Package service contains application use cases: triage pipeline, task management, support desk,
// runtime settings, backups and background scheduling.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"tgtriage/internal/domain"
)

const metaPrefix = "meta."

const (
	keyChain = "ai.chain" // JSON array of domain.AIKey
	// keyLegacyProvider is the pre-chain "active provider" override. Still honored — that provider's
	// keys move to the front of the env chain — until the chain itself is saved.
	keyLegacyProvider = "ai.provider"
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

// EnvLookup reads an environment variable (os.LookupEnv in production).
type EnvLookup func(string) (string, bool)

// SettingsService keeps runtime settings in memory, persisted in the DB. Built-in defaults apply to
// keys absent from the DB; on start, legacy env variables are imported for such keys once, so an
// existing .env keeps working and can then be trimmed down to the bootstrap variables.
type SettingsService struct {
	repo domain.SettingsRepository
	env  EnvLookup
	log  *slog.Logger

	mu        sync.RWMutex
	current   domain.Settings
	loc       *time.Location
	listeners []func(old, next domain.Settings)
}

func NewSettingsService(ctx context.Context, repo domain.SettingsRepository, env EnvLookup, log *slog.Logger) (*SettingsService, error) {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &SettingsService{repo: repo, env: env, log: log.With("component", "settings")}
	if err := s.importEnv(ctx); err != nil {
		return nil, err
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SettingsService) envValue(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	v, ok := s.env(name)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// importEnv stores values of legacy env variables for settings the DB does not have yet.
func (s *SettingsService) importEnv(ctx context.Context) error {
	values, err := s.repo.All(ctx)
	if err != nil {
		return err
	}
	toStore := map[string]string{}
	for _, f := range settingFields {
		if _, ok := values[f.Key]; ok {
			continue
		}
		v, ok := s.envValue(f.Env)
		if !ok {
			continue
		}
		tmp := DefaultSettings()
		if err := f.set(&tmp, v); err != nil {
			s.log.Warn("ignoring invalid env value", "env", f.Env, "err", err)
			continue
		}
		toStore[f.Key] = f.get(&tmp)
	}
	if _, ok := values[keyChain]; !ok {
		primary, _ := s.envValue("AI_PROVIDER")
		chain := envChain(strings.ToLower(primary), s.envValue)
		if p := values[keyLegacyProvider]; p != "" {
			chain = promote(chain, p)
		}
		if len(chain) > 0 {
			b, _ := json.Marshal(chain)
			toStore[keyChain] = string(b)
		}
	}
	if len(toStore) == 0 {
		return nil
	}
	s.log.Info("imported settings from environment", "count", len(toStore))
	return s.repo.SetMany(ctx, toStore)
}

// envChain orders the env keys into the initial AI chain: the primary provider's keys first, then the
// other providers in domain.Providers order, each provider's keys in the order they were listed.
func envChain(primary string, env func(string) (string, bool)) []domain.AIKey {
	vars := map[string]string{
		domain.ProviderClaude: "ANTHROPIC_API_KEY", domain.ProviderGemini: "GEMINI_API_KEY",
		domain.ProviderGroq: "GROQ_API_KEY", domain.ProviderMistral: "MISTRAL_API_KEY",
		domain.ProviderOpenRouter: "OPENROUTER_API_KEY",
	}
	if !domain.KnownProvider(primary) {
		primary = domain.ProviderClaude
	}
	var chain []domain.AIKey
	for i, p := range append([]string{primary}, domain.Providers...) {
		if i > 0 && p == primary {
			continue
		}
		v, ok := env(vars[p])
		if !ok {
			continue
		}
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				chain = append(chain, domain.AIKey{Provider: p, Key: k})
			}
		}
	}
	return chain
}

func (s *SettingsService) reload(ctx context.Context) error {
	values, err := s.repo.All(ctx)
	if err != nil {
		return err
	}
	cur := DefaultSettings()
	for _, f := range settingFields {
		v, ok := values[f.Key]
		if !ok {
			continue
		}
		if err := f.set(&cur, v); err != nil {
			s.log.Warn("ignoring invalid stored setting", "key", f.Key, "err", err)
		}
	}
	if v := values[keyChain]; v != "" {
		var chain []domain.AIKey
		if err := json.Unmarshal([]byte(v), &chain); err == nil {
			cur.AIChain = chain
		}
	}
	s.mu.Lock()
	old := s.current
	s.current = cur
	s.loc = cur.Location()
	listeners := slices.Clone(s.listeners)
	s.mu.Unlock()
	for _, fn := range listeners {
		fn(old, cur)
	}
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

// Location returns the service timezone.
func (s *SettingsService) Location() *time.Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loc == nil {
		return time.UTC
	}
	return s.loc
}

// OnChange registers a listener called after every successful change (outside the lock).
func (s *SettingsService) OnChange(fn func(old, next domain.Settings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// encodeSettings serializes every persisted field to its storage representation.
func encodeSettings(st *domain.Settings) map[string]string {
	chain, _ := json.Marshal(st.AIChain) // a slice of plain string structs always marshals
	out := make(map[string]string, len(settingFields)+1)
	out[keyChain] = string(chain)
	for _, f := range settingFields {
		out[f.Key] = f.get(st)
	}
	return out
}

func cloneSettings(st domain.Settings) domain.Settings {
	st.AIChain = slices.Clone(st.AIChain)
	for _, p := range domain.Providers {
		ps := st.Provider(p)
		ps.Presets = slices.Clone(ps.Presets)
	}
	return st
}

// Update applies fn atomically, validates and persists only the fields that actually changed.
func (s *SettingsService) Update(ctx context.Context, fn func(*domain.Settings)) (domain.Settings, error) {
	return s.UpdateE(ctx, func(st *domain.Settings) error { fn(st); return nil })
}

// UpdateE is Update with a mutation that may fail.
func (s *SettingsService) UpdateE(ctx context.Context, fn func(*domain.Settings) error) (domain.Settings, error) {
	s.mu.Lock()
	next := cloneSettings(s.current)
	if err := fn(&next); err != nil {
		s.mu.Unlock()
		return s.Get(), err
	}
	next = cloneSettings(next) // fn may have put in slices the caller still holds
	for i := range next.AIChain {
		next.AIChain[i].Provider = strings.ToLower(strings.TrimSpace(next.AIChain[i].Provider))
		next.AIChain[i].Key = strings.TrimSpace(next.AIChain[i].Key)
	}
	if err := validate(&next); err != nil {
		cur := s.current
		s.mu.Unlock()
		return cur, err
	}
	before, after := encodeSettings(&s.current), encodeSettings(&next)
	changed := make(map[string]string, len(after))
	for k, v := range after {
		if before[k] != v {
			changed[k] = v
		}
	}
	if len(changed) > 0 {
		if err := s.repo.SetMany(ctx, changed); err != nil {
			cur := s.current
			s.mu.Unlock()
			return cur, err
		}
	}
	old := s.current
	s.current = next
	s.loc = next.Location()
	listeners := slices.Clone(s.listeners)
	s.mu.Unlock()
	if len(changed) > 0 {
		for _, l := range listeners {
			l(old, next)
		}
	}
	return next, nil
}

// UpdateFields applies JSON values keyed by setting key (Mini App settings form).
func (s *SettingsService) UpdateFields(ctx context.Context, values map[string]json.RawMessage) (domain.Settings, error) {
	return s.UpdateE(ctx, func(st *domain.Settings) error {
		for key, raw := range values {
			f, ok := fieldByKey(key)
			if !ok {
				return fmt.Errorf("%w: неизвестная настройка %q", domain.ErrInvalidInput, key)
			}
			if err := f.SetJSON(st, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// validate round-trips every field through its parser, which carries the field's validation rules.
func validate(st *domain.Settings) error {
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
	probe := DefaultSettings()
	for _, f := range settingFields {
		if err := f.set(&probe, f.get(st)); err != nil {
			return err
		}
	}
	return nil
}

// Reset drops all overrides, re-imports env variables and returns to defaults for the rest.
func (s *SettingsService) Reset(ctx context.Context) error {
	if err := s.repo.DeleteExceptPrefix(ctx, metaPrefix); err != nil {
		return err
	}
	if err := s.importEnv(ctx); err != nil {
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
