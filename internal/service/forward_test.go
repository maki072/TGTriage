package service

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
	"tgtriage/internal/repository/sqlite"
)

type fakeProvider struct {
	name string // defaults to claude
	reqs chan ai.Request
	resp string
	err  error
}

func (p *fakeProvider) Name() string {
	if p.name == "" {
		return domain.ProviderClaude
	}
	return p.name
}

func (p *fakeProvider) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.reqs <- req
	if p.err != nil {
		return nil, p.err
	}
	return &ai.Response{Text: p.resp}, nil
}

type chanNotifier struct{ created chan *domain.Task }

func (n chanNotifier) TaskCreated(_ context.Context, t *domain.Task)                { n.created <- t }
func (chanNotifier) TaskUpdated(context.Context, *domain.Task)                      {}
func (chanNotifier) AnalysisFailed(context.Context, *domain.AnalysisRecord, string) {}
func (chanNotifier) ForwardFailed(context.Context, *domain.AnalysisRecord)          {}

const forwardTaskJSON = `{"message_type":"task","analysis":"","is_task":true,
	"confidence":0.9,"update_task_id":0,"title":"Отправить отчёт Ивану","description":"","priority":"high",
	"category":"task","deadline":"","reply_strategy":"none","draft_reply":""}`

func newForwardTestService(t *testing.T, defaults domain.Settings, cfg TriageConfig, providers ...ai.Provider) (*TriageService, *SettingsService, *sqlite.Store, chanNotifier) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings, err := NewSettingsService(ctx, newMemSettingsRepo(), defaults)
	if err != nil {
		t.Fatal(err)
	}
	registry := ai.NewRegistry()
	for _, p := range providers {
		registry.Register(p)
	}
	n := chanNotifier{created: make(chan *domain.Task, 4)}
	s := NewTriageService(cfg, store.Messages, store.Tasks, store.Analyses,
		NewConnectionService(store.Connections, 1), settings, registry, n, slog.New(slog.DiscardHandler))
	return s, settings, store, n
}

func waitTask(t *testing.T, n chanNotifier) *domain.Task {
	t.Helper()
	select {
	case task := <-n.created:
		return task
	case <-time.After(10 * time.Second):
		t.Fatal("no task created")
		return nil
	}
}

func TestForwardedBatchBecomesOneTask(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{reqs: make(chan ai.Request, 4), resp: forwardTaskJSON}
	s, settings, store, n := newForwardTestService(t, defaultTestSettings(), TriageConfig{}, p)

	// A paused triage must not block an explicit forward.
	if _, err := settings.Update(ctx, func(st *domain.Settings) { st.TriagePaused = true }); err != nil {
		t.Fatal(err)
	}
	s.OnForwarded(domain.Message{SenderName: "Я", Outgoing: true, SenderID: 1, Text: "Напомни себе", SentAt: time.Now()})
	s.OnForwarded(domain.Message{SenderName: "Иван", SenderUsername: "ivan", SenderID: 7, Text: "Скинь отчёт", SentAt: time.Now()})

	task := waitTask(t, n)
	if len(p.reqs) != 1 {
		t.Fatalf("expected one LLM call for the whole batch, got %d", len(p.reqs))
	}
	req := <-p.reqs
	if !strings.Contains(req.User, "OWNER: Напомни себе") || !strings.Contains(req.User, "Иван (@ivan): Скинь отчёт") {
		t.Errorf("batch not in prompt:\n%s", req.User)
	}
	if task.HasChat() || task.SenderID != 7 || task.SenderUsername != "ivan" {
		t.Errorf("task should have no chat and be attributed to the non-owner author: %+v", task)
	}
	if task.Title != "Отправить отчёт Ивану" || task.Priority != domain.PriorityHigh || task.DraftReply != "" {
		t.Errorf("analysis not applied: %+v", task)
	}
	if task.SourceText != "Я: Напомни себе\nИван: Скинь отчёт" {
		t.Errorf("source text: %q", task.SourceText)
	}
	stored, err := store.Tasks.Get(ctx, task.ID)
	if err != nil || stored.AnalysisID == 0 {
		t.Fatalf("task not stored with analysis link: %+v, %v", stored, err)
	}
}

// TestChainFallsBackToNextKey: an HTTP error on one chain entry hands the call to the next entry
// right away (no retries burned on a key that is out of quota or region-blocked).
func TestChainFallsBackToNextKey(t *testing.T) {
	blocked := &fakeProvider{name: domain.ProviderGemini, reqs: make(chan ai.Request, 8),
		err: &ai.Error{Provider: domain.ProviderGemini, Status: 429, Message: "quota", Retryable: true}}
	working := &fakeProvider{name: domain.ProviderGroq, reqs: make(chan ai.Request, 8), resp: forwardTaskJSON}
	defaults := defaultTestSettings()
	defaults.AIChain = []domain.AIKey{
		{Provider: domain.ProviderGemini, Key: "AIza-test-gemini-01"},
		{Provider: domain.ProviderGroq, Key: "gsk-test-groq-0001"},
	}
	s, _, _, n := newForwardTestService(t, defaults, TriageConfig{MaxRetries: 3}, blocked, working)

	s.OnForwarded(domain.Message{SenderName: "Иван", SenderID: 7, Text: "Скинь отчёт", SentAt: time.Now()})

	task := waitTask(t, n)
	if task.Provider != domain.ProviderGroq || task.Model != defaults.GroqModel {
		t.Errorf("task must be attributed to the entry that answered, got %s / %s", task.Provider, task.Model)
	}
	if len(blocked.reqs) != 1 {
		t.Errorf("failed entry must be tried once before falling back, got %d calls", len(blocked.reqs))
	}
	if req := <-working.reqs; req.APIKey != "gsk-test-groq-0001" || req.Model != defaults.GroqModel {
		t.Errorf("fallback entry must get its own key and model, got key %q model %q", req.APIKey, req.Model)
	}
}
