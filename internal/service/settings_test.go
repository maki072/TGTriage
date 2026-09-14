package service

import (
	"context"
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

func defaultTestSettings() domain.Settings {
	return domain.Settings{
		ActiveProvider:  domain.ProviderClaude,
		ClaudeModel:     "claude-opus-5",
		GeminiModel:     "gemini-3.6-flash",
		GroqModel:       "openai/gpt-oss-120b",
		MistralModel:    "mistral-small-latest",
		OpenRouterModel: "openai/gpt-oss-20b:free",
		DebounceSeconds: 20,
		Sensitivity:     domain.SensitivityMedium,
		DigestEnabled:   true,
		DigestTime:      "09:00",
		MarkReadOnWork:  true,
	}
}

// TestUpdateWritesOnlyChangedFields is a regression test: a single-field change must not
// freeze every other field in the DB, or later env-file edits to those fields stop applying.
func TestUpdateWritesOnlyChangedFields(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc, err := NewSettingsService(ctx, repo, defaultTestSettings(), []string{domain.ProviderClaude, domain.ProviderGemini})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = 45 }); err != nil {
		t.Fatal(err)
	}
	if len(repo.data) != 1 {
		t.Fatalf("expected exactly 1 persisted key after a single-field change, got %d: %v", len(repo.data), repo.data)
	}
	if repo.data[keyDebounce] != "45" {
		t.Errorf("debounce not persisted: %v", repo.data)
	}

	// Simulate redeploying with a new env default for a field the user never touched in the bot.
	newDefaults := defaultTestSettings()
	newDefaults.GeminiModel = "gemini-3.8-flash"
	svc2, err := NewSettingsService(ctx, repo, newDefaults, []string{domain.ProviderClaude, domain.ProviderGemini})
	if err != nil {
		t.Fatal(err)
	}
	got := svc2.Get()
	if got.GeminiModel != "gemini-3.8-flash" {
		t.Errorf("untouched field must keep following the .env default, got %q", got.GeminiModel)
	}
	if got.DebounceSeconds != 45 {
		t.Errorf("field changed via Update must survive redeploy, got %d", got.DebounceSeconds)
	}
}

func TestUpdateNoopWritesNothing(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc, err := NewSettingsService(ctx, repo, defaultTestSettings(), []string{domain.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, func(*domain.Settings) {}); err != nil {
		t.Fatal(err)
	}
	if len(repo.data) != 0 {
		t.Errorf("a no-op update must not write anything, got %v", repo.data)
	}
}

func TestUpdateRejectsInvalidAndKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc, err := NewSettingsService(ctx, repo, defaultTestSettings(), []string{domain.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = -1 })
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got := svc.Get().DebounceSeconds; got != 20 {
		t.Errorf("invalid update must not change current settings, got %d", got)
	}
	if len(repo.data) != 0 {
		t.Errorf("invalid update must not persist anything, got %v", repo.data)
	}
}

func TestResetDropsOverridesButKeepsMeta(t *testing.T) {
	ctx := context.Background()
	repo := newMemSettingsRepo()
	svc, err := NewSettingsService(ctx, repo, defaultTestSettings(), []string{domain.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetMeta(ctx, "last_digest", "2026-09-14"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, func(s *domain.Settings) { s.DebounceSeconds = 99 }); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if got := svc.Get().DebounceSeconds; got != 20 {
		t.Errorf("reset must restore the .env default, got %d", got)
	}
	if v, err := svc.Meta(ctx, "last_digest"); err != nil || v != "2026-09-14" {
		t.Errorf("reset must keep meta.* keys, got %q err=%v", v, err)
	}
}
