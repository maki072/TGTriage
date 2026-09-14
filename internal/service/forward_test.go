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
	reqs chan ai.Request
	resp string
}

func (p *fakeProvider) Name() string { return domain.ProviderClaude }

func (p *fakeProvider) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.reqs <- req
	return &ai.Response{Text: p.resp}, nil
}

type chanNotifier struct{ created chan *domain.Task }

func (n chanNotifier) TaskCreated(_ context.Context, t *domain.Task)                { n.created <- t }
func (chanNotifier) TaskUpdated(context.Context, *domain.Task)                      {}
func (chanNotifier) AnalysisFailed(context.Context, *domain.AnalysisRecord, string) {}
func (chanNotifier) ForwardFailed(context.Context, *domain.AnalysisRecord)          {}

func TestForwardedBatchBecomesOneTask(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings, err := NewSettingsService(ctx, newMemSettingsRepo(), defaultTestSettings(), []string{domain.ProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{reqs: make(chan ai.Request, 4), resp: `{"message_type":"task","analysis":"","is_task":true,
		"confidence":0.9,"update_task_id":0,"title":"Отправить отчёт Ивану","description":"","priority":"high",
		"category":"task","deadline":"","reply_strategy":"none","draft_reply":""}`}
	registry := ai.NewRegistry()
	registry.Register(p)
	n := chanNotifier{created: make(chan *domain.Task, 4)}
	s := NewTriageService(TriageConfig{}, store.Messages, store.Tasks, store.Analyses,
		NewConnectionService(store.Connections, 1), settings, registry, n, slog.New(slog.DiscardHandler))

	// A paused triage must not block an explicit forward.
	if _, err := settings.Update(ctx, func(st *domain.Settings) { st.TriagePaused = true }); err != nil {
		t.Fatal(err)
	}
	s.OnForwarded(domain.Message{SenderName: "Я", Outgoing: true, SenderID: 1, Text: "Напомни себе", SentAt: time.Now()})
	s.OnForwarded(domain.Message{SenderName: "Иван", SenderUsername: "ivan", SenderID: 7, Text: "Скинь отчёт", SentAt: time.Now()})

	var task *domain.Task
	select {
	case task = <-n.created:
	case <-time.After(10 * time.Second):
		t.Fatal("no task created")
	}
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
