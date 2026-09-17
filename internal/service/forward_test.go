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

func newForwardTestService(t *testing.T, env map[string]string, providers ...ai.Provider) (*TriageService, *SettingsService, *sqlite.Store, chanNotifier) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := newTestSettings(t, newMemSettingsRepo(), env)
	registry := ai.NewRegistry()
	for _, p := range providers {
		registry.Register(p)
	}
	n := chanNotifier{created: make(chan *domain.Task, 4)}
	s := NewTriageService(store.Messages, store.Tasks, store.Analyses,
		NewConnectionService(store.Connections, 1), settings, nil, registry, n, slog.New(slog.DiscardHandler))
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
	s, settings, store, n := newForwardTestService(t, map[string]string{"ANTHROPIC_API_KEY": "sk-test-claude-0001"}, p)

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
	s, settings, _, n := newForwardTestService(t, map[string]string{
		"AI_PROVIDER": "gemini", "GEMINI_API_KEY": "AIza-test-gemini-01", "GROQ_API_KEY": "gsk-test-groq-0001", "AI_MAX_RETRIES": "3",
	}, blocked, working)

	s.OnForwarded(domain.Message{SenderName: "Иван", SenderID: 7, Text: "Скинь отчёт", SentAt: time.Now()})

	task := waitTask(t, n)
	groqModel := settings.Get().Groq.Model
	if task.Provider != domain.ProviderGroq || task.Model != groqModel {
		t.Errorf("task must be attributed to the entry that answered, got %s / %s", task.Provider, task.Model)
	}
	if len(blocked.reqs) != 1 {
		t.Errorf("failed entry must be tried once before falling back, got %d calls", len(blocked.reqs))
	}
	if req := <-working.reqs; req.APIKey != "gsk-test-groq-0001" || req.Model != groqModel {
		t.Errorf("fallback entry must get its own key and model, got key %q model %q", req.APIKey, req.Model)
	}
}

const helpdeskTicketJSON = `{"message_type":"bug","analysis":"","is_task":true,
	"confidence":0.8,"update_task_id":0,"title":"Не проходит оплата картой","description":"Ошибка при оплате","priority":"high",
	"category":"bug","deadline":"","reply_strategy":"clarify","draft_reply":"Уточните, пожалуйста, номер заказа."}`

func TestHelpdeskTicketUsesSupportPrompt(t *testing.T) {
	p := &fakeProvider{reqs: make(chan ai.Request, 4), resp: helpdeskTicketJSON}
	s, _, _, n := newForwardTestService(t, map[string]string{"ANTHROPIC_API_KEY": "sk-test-claude-0001", "HELPDESK_ABOUT": "Интернет-магазин"}, p)
	u := &domain.HelpdeskUser{UserID: 42, Name: "Анна", Username: "anna"}
	task, err := s.CreateHelpdeskTicket(context.Background(), u, []domain.Message{
		{MessageID: 7, Text: "Не проходит оплата", SentAt: time.Now()},
		{MessageID: 8, Outgoing: true, Text: "Какая ошибка?", SentAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-n.created
	req := <-p.reqs
	if !strings.Contains(req.System, "службы поддержки") || !strings.Contains(req.System, "Интернет-магазин") ||
		!strings.Contains(req.User, "SUPPORT: Какая ошибка?") || !strings.Contains(req.User, "Анна (@anna): Не проходит оплата") {
		t.Errorf("support prompt expected:\n%s\n%s", req.System, req.User)
	}
	if !task.IsHelpdesk() || task.ChatID != 42 || task.DraftReply == "" || len(task.SourceMessageIDs) != 1 || task.SourceMessageIDs[0] != 7 {
		t.Errorf("ticket fields: %+v", task)
	}
}

func TestHelpdeskTicketWithoutAIStillCreated(t *testing.T) {
	s, _, _, n := newForwardTestService(t, nil)
	u := &domain.HelpdeskUser{UserID: 42, Name: "Анна"}
	task, err := s.CreateHelpdeskTicket(context.Background(), u, []domain.Message{{Text: "Верните деньги за заказ 123", SentAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	<-n.created
	if task.Title != "Верните деньги за заказ 123" || task.Priority != domain.PriorityMedium || !task.IsHelpdesk() {
		t.Errorf("an operator's ticket must be created even without AI: %+v", task)
	}
}

// TestTriagePauseCoversPersonalChatsOnly: the bot's triage pause is about Business chats; support desk
// messages follow the helpdesk's own "automatic tickets" switch.
func TestTriagePauseCoversPersonalChatsOnly(t *testing.T) {
	ctx := context.Background()
	s, settings, store, _ := newForwardTestService(t, nil)
	if _, err := settings.Update(ctx, func(st *domain.Settings) { st.TriagePaused = true }); err != nil {
		t.Fatal(err)
	}
	incoming := func(conn string, chat int64, id int) *domain.Message {
		t.Helper()
		m := &domain.Message{ConnectionID: conn, ChatID: chat, MessageID: id, Text: "Не работает 1С", SentAt: time.Now()}
		if err := s.OnIncoming(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, err := store.Messages.Find(ctx, conn, chat, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if m := incoming("biz", 7, 1); !m.Analyzed {
		t.Error("a paused triage must skip personal messages")
	}
	if m := incoming(domain.HelpdeskConnectionID, 42, 1); m.Analyzed {
		t.Error("the personal triage pause must not stop helpdesk triage")
	}
	if _, err := settings.Update(ctx, func(st *domain.Settings) { st.Helpdesk.TriageEnabled = false }); err != nil {
		t.Fatal(err)
	}
	if m := incoming(domain.HelpdeskConnectionID, 42, 2); !m.Analyzed {
		t.Error("helpdesk messages must be skipped when automatic tickets are off")
	}
}
