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

type notice struct {
	task   *domain.Task
	silent bool
}

type recNotifier struct{ created, updated chan notice }

func (n recNotifier) TaskCreated(ctx context.Context, t *domain.Task) {
	n.created <- notice{t, IsSilent(ctx)}
}
func (n recNotifier) TaskUpdated(ctx context.Context, t *domain.Task) {
	n.updated <- notice{t, IsSilent(ctx)}
}
func (recNotifier) AnalysisFailed(context.Context, *domain.AnalysisRecord, string) {}
func (recNotifier) ForwardFailed(context.Context, *domain.AnalysisRecord)          {}

func newQuietTestService(t *testing.T, p *fakeProvider) (*TriageService, *sqlite.Store, recNotifier) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := newTestSettings(t, newMemSettingsRepo(), map[string]string{"ANTHROPIC_API_KEY": "sk-test-claude-0001"})
	if _, err := settings.Update(ctx, func(st *domain.Settings) {
		st.DebounceSeconds, st.DebounceMaxWaitSeconds = 1, 1
		st.ThreadMinutes, st.QuietActiveMinutes = 120, 15
		st.NoisePrefilter = false
	}); err != nil {
		t.Fatal(err)
	}
	registry := ai.NewRegistry()
	registry.Register(p)
	n := recNotifier{created: make(chan notice, 8), updated: make(chan notice, 8)}
	s := NewTriageService(store.Messages, store.Tasks, store.Analyses,
		NewConnectionService(store.Connections, 1), settings, nil, registry, n, slog.New(slog.DiscardHandler))
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return s, store, n
}

func waitNotice(t *testing.T, ch chan notice) notice {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(15 * time.Second):
		t.Fatal("no notification")
		return notice{}
	}
}

func TestCredentialsNeverReachTheModel(t *testing.T) {
	p := &fakeProvider{reqs: make(chan ai.Request, 4), resp: forwardTaskJSON}
	s, _, n := newQuietTestService(t, p)
	text := "Войди в 1С: логин: ivan.p пароль: Zx9!kLm2, anydesk 409 627 158, почта boss@example.com, " +
		"ключ sk-ant-api03-AbCdEfGhIjKlMnOpQrSt"
	if err := s.OnIncoming(context.Background(), &domain.Message{ConnectionID: "biz", ChatID: 7, MessageID: 1,
		SenderID: 7, SenderName: "Иван", Text: text, SentAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	req := <-p.reqs
	for _, secret := range []string{"ivan.p", "Zx9!kLm2", "409 627 158", "boss@example.com", "AbCdEfGhIjKlMnOp"} {
		if strings.Contains(req.User, secret) || strings.Contains(req.System, secret) {
			t.Errorf("%q reached the LLM prompt:\n%s", secret, req.User)
		}
	}
	if !strings.Contains(req.User, "Войди в 1С") {
		t.Errorf("the rest of the message must stay readable:\n%s", req.User)
	}
	// the owner's own card still shows the original text
	if task := waitNotice(t, n.created).task; !strings.Contains(task.SourceText, "Zx9!kLm2") {
		t.Errorf("stored source text must stay intact: %q", task.SourceText)
	}
}

func TestFollowUpJoinsOpenTaskSilently(t *testing.T) {
	p := &fakeProvider{reqs: make(chan ai.Request, 8), resp: forwardTaskJSON}
	s, store, n := newQuietTestService(t, p)
	ctx := context.Background()
	send := func(id int, text string) {
		t.Helper()
		if err := s.OnIncoming(ctx, &domain.Message{ConnectionID: "biz", ChatID: 7, MessageID: id,
			SenderID: 7, SenderName: "Иван", Text: text, SentAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	send(1, "Не открывается 1С")
	first := waitNotice(t, n.created)
	send(2, "И ещё принтер не печатает")
	upd := waitNotice(t, n.updated)
	if upd.task.ID != first.task.ID {
		t.Errorf("follow-up opened task %d instead of joining %d", upd.task.ID, first.task.ID)
	}
	if !upd.silent {
		t.Error("an update of an existing card must not ring")
	}
	if _, total, err := store.Tasks.List(ctx, domain.TaskFilter{ChatID: 7, Limit: 10}); err != nil || total != 1 {
		t.Errorf("want one task for the conversation, got %d (%v)", total, err)
	}
}

func TestThreadTask(t *testing.T) {
	now := time.Now()
	open := []domain.Task{
		{ID: 1, Status: domain.StatusNew, CreatedAt: now.Add(-5 * time.Hour), UpdatedAt: now.Add(-5 * time.Hour)},
		{ID: 2, Status: domain.StatusInProgress, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: 3, Status: domain.StatusSnoozed, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
	}
	if got := threadTask(open, now, 2*time.Hour); got == nil || got.ID != 2 {
		t.Errorf("want task 2 (recently touched, not snoozed), got %+v", got)
	}
	if got := threadTask(open, now, 5*time.Minute); got != nil {
		t.Errorf("nothing was touched within 5 minutes, got %+v", got)
	}
	if got := threadTask(open, now, 0); got != nil {
		t.Errorf("window 0 switches the feature off, got %+v", got)
	}
}

func TestOwnerActive(t *testing.T) {
	now := time.Now()
	h := []domain.Message{
		{Outgoing: false, SentAt: now.Add(-time.Minute)},
		{Outgoing: true, SentAt: now.Add(-10 * time.Minute)},
	}
	if !ownerActive(h, now, 15) {
		t.Error("owner wrote 10 minutes ago, window 15")
	}
	if ownerActive(h, now, 5) {
		t.Error("owner wrote 10 minutes ago, window 5")
	}
	if ownerActive(h, now, 0) {
		t.Error("window 0 switches the feature off")
	}
}
