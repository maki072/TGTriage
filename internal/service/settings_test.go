package service

import (
	"context"
	"encoding/json"
	"testing"

	"tgtriage/internal/domain"
)

// memSettingsRepo is a minimal in-memory domain.SettingsRepository for tests.
type memSettingsRepo struct{ data map[string]string }

func newMemSettingsRepo() *memSettingsRepo { return &memSettingsRepo{data: map[string]string{}} }

func (r *memSettingsRepo) All(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.data))
	for k, v := range r.data {
		out[k] = v
	}
	return out, nil
}

func (r *memSettingsRepo) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := r.data[key]
	return v, ok, nil
}

func (r *memSettingsRepo) Set(ctx context.Context, key, value string) error {
	return r.SetMany(ctx, map[string]string{key: value})
}

func (r *memSettingsRepo) SetMany(_ context.Context, values map[string]string) error {
	for k, v := range values {
		r.data[k] = v
	}
	return nil
}

func (r *memSettingsRepo) DeleteExceptPrefix(_ context.Context, keep string) error {
	for k := range r.data {
		if len(k) < len(keep) || k[:len(keep)] != keep {
			delete(r.data, k)
		}
	}
	return nil
}

var _ domain.SettingsRepository = (*memSettingsRepo)(nil)

func envOf(m map[string]string) EnvLookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func newTestSettings(t *testing.T, repo *memSettingsRepo, env map[string]string) *SettingsService {
	t.Helper()
	svc, err := NewSettingsService(context.Background(), repo, envOf(env), nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestDefaultsAreValid(t *testing.T) {
	st := DefaultSettings()
	if err := validate(&st); err != nil {
		t.Fatalf("built-in defaults must pass validation: %v", err)
	}
	if st.DebounceSeconds != 20 || st.Timezone != "Europe/Moscow" || len(st.Claude.Presets) == 0 || st.Helpdesk.Enabled {
		t.Errorf("unexpected defaults: %+v", st)
	}
}

// TestEnvImportedOnceThenDBWins: legacy env variables seed the DB, after which the DB is the source of
// truth and the .env can be trimmed down to bootstrap variables.
func TestEnvImportedOnceThenDBWins(t *testing.T) {
	repo := newMemSettingsRepo()
	svc := newTestSettings(t, repo, map[string]string{
		"DEBOUNCE_SECONDS": "45", "ANTHROPIC_API_KEY": "sk-test-claude-0001", "AI_TIMEOUT": "45s", "SENSITIVITY": "bogus",
	})
	st := svc.Get()
	if st.DebounceSeconds != 45 || st.AITimeoutSec != 45 || len(st.AIChain) != 1 {
		t.Fatalf("env not imported: %+v", st)
	}
	if st.Sensitivity != domain.SensitivityMedium {
		t.Errorf("invalid env value must be ignored, got %q", st.Sensitivity)
	}
	if repo.data["triage.debounce_seconds"] != "45" || repo.data[keyChain] == "" {
		t.Errorf("imported values must be persisted: %v", repo.data)
	}

	svc2 := newTestSettings(t, repo, map[string]string{"DEBOUNCE_SECONDS": "10"}) // env edited, keys removed
	if got := svc2.Get(); got.DebounceSeconds != 45 || len(got.AIChain) != 1 {
		t.Errorf("DB values must win over later env edits: %+v", got)
	}
}

func TestUpdateWritesOnlyChangedFields(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc := newTestSettings(t, repo, nil)
	if _, err := svc.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = 45 }); err != nil {
		t.Fatal(err)
	}
	if len(repo.data) != 1 || repo.data["triage.debounce_seconds"] != "45" {
		t.Fatalf("expected exactly the changed key persisted, got %v", repo.data)
	}
}

func TestUpdateNoopWritesNothing(t *testing.T) {
	repo := newMemSettingsRepo()
	svc := newTestSettings(t, repo, nil)
	if _, err := svc.Update(context.Background(), func(*domain.Settings) {}); err != nil {
		t.Fatal(err)
	}
	if len(repo.data) != 0 {
		t.Errorf("a no-op update must not write anything, got %v", repo.data)
	}
}

func TestUpdateRejectsInvalidAndKeepsCurrent(t *testing.T) {
	repo := newMemSettingsRepo()
	svc := newTestSettings(t, repo, nil)
	if _, err := svc.Update(context.Background(), func(s *domain.Settings) { s.DebounceSeconds = -1 }); err == nil {
		t.Fatal("expected validation error")
	}
	if got := svc.Get().DebounceSeconds; got != 20 {
		t.Errorf("invalid update must not change current settings, got %d", got)
	}
	if len(repo.data) != 0 {
		t.Errorf("invalid update must not persist anything, got %v", repo.data)
	}
}

func TestResetReimportsEnvAndKeepsMeta(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc := newTestSettings(t, repo, map[string]string{"DEBOUNCE_SECONDS": "30"})
	if err := svc.SetMeta(ctx, "last_digest", "2026-09-14"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = 99; s.DigestTime = "07:00" }); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if got := svc.Get(); got.DebounceSeconds != 30 || got.DigestTime != "09:00" {
		t.Errorf("reset must restore env values and defaults, got debounce=%d digest=%s", got.DebounceSeconds, got.DigestTime)
	}
	if v, err := svc.Meta(ctx, "last_digest"); err != nil || v != "2026-09-14" {
		t.Errorf("reset must keep meta.* keys, got %q err=%v", v, err)
	}
}

func TestLegacyProviderOverridePromotesItsKeys(t *testing.T) {
	repo := newMemSettingsRepo()
	repo.data[keyLegacyProvider] = domain.ProviderGemini
	svc := newTestSettings(t, repo, map[string]string{
		"ANTHROPIC_API_KEY": "sk-test-claude-0001", "GEMINI_API_KEY": "AIza-test-gemini-01",
	})
	if p, _ := svc.Get().Primary(); p.Provider != domain.ProviderGemini {
		t.Errorf("provider chosen before the chain existed must stay first, got chain %+v", svc.Get().AIChain)
	}
}

func TestChainPersistsAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc := newTestSettings(t, repo, nil)
	chain := []domain.AIKey{
		{Provider: " Groq ", Key: " gsk-test-groq-0001 "},
		{Provider: domain.ProviderClaude, Key: "sk-test-claude-0001"},
	}
	if _, err := svc.Update(ctx, func(s *domain.Settings) { s.AIChain = chain }); err != nil {
		t.Fatal(err)
	}
	if chain[0].Key != " gsk-test-groq-0001 " {
		t.Error("Update must not mutate the caller's slice")
	}
	got := newTestSettings(t, repo, nil).Get().AIChain
	if len(got) != 2 || got[0] != (domain.AIKey{Provider: domain.ProviderGroq, Key: "gsk-test-groq-0001"}) {
		t.Errorf("saved chain must be normalized and reloaded as is, got %+v", got)
	}
}

func TestUpdateRejectsInvalidChain(t *testing.T) {
	ctx := context.Background()
	svc := newTestSettings(t, newMemSettingsRepo(), nil)
	for name, chain := range map[string][]domain.AIKey{
		"unknown provider": {{Provider: "openai", Key: "sk-test-0000000001"}},
		"empty key":        {{Provider: domain.ProviderGemini, Key: "  "}},
		"key with space":   {{Provider: domain.ProviderGemini, Key: "AIza test"}},
	} {
		if _, err := svc.Update(ctx, func(s *domain.Settings) { s.AIChain = chain }); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if _, err := svc.Update(ctx, func(s *domain.Settings) { s.AIChain = nil }); err != nil {
		t.Errorf("an empty chain must be allowed: %v", err)
	}
}

func TestUpdateFieldsJSON(t *testing.T) {
	ctx := context.Background()
	svc := newTestSettings(t, newMemSettingsRepo(), nil)
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

	st, err := svc.UpdateFields(ctx, map[string]json.RawMessage{
		"helpdesk.enabled":    raw(true),
		"helpdesk.group_id":   raw(int64(-1001234567890)),
		"helpdesk.hours_days": raw("531"),
		"ai.claude_presets":   raw([]string{"claude-opus-5", " claude-haiku-4-5 "}),
		"general.timezone":    raw("Asia/Yekaterinburg"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Helpdesk.Active() || st.Helpdesk.HoursDays != "135" || len(st.Claude.Presets) != 2 || st.Claude.Presets[1] != "claude-haiku-4-5" {
		t.Errorf("fields not applied: %+v", st.Helpdesk)
	}
	if svc.Location().String() != "Asia/Yekaterinburg" {
		t.Errorf("location must follow the timezone setting, got %s", svc.Location())
	}

	for name, values := range map[string]map[string]json.RawMessage{
		"positive group id": {"helpdesk.group_id": raw(5)},
		"bad timezone":      {"general.timezone": raw("Mars/Base")},
		"unknown key":       {"nope": raw(1)},
		"http public url":   {"webapp.public_url": raw("http://example.com")},
		"type mismatch":     {"helpdesk.enabled": raw("yes")},
	} {
		if _, err := svc.UpdateFields(ctx, values); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if got := svc.Get().Helpdesk.GroupID; got != -1001234567890 {
		t.Errorf("failed update must keep previous values, got %d", got)
	}
}
