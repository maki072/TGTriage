package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tgtriage/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMessagesLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now()

	var ids []int64
	for i := 1; i <= 3; i++ {
		m := &domain.Message{ConnectionID: "c1", ChatID: 42, MessageID: i, SenderID: 42, SenderName: "Пётр", Text: "msg", SentAt: now}
		ok, err := s.Messages.Save(ctx, m)
		if err != nil || !ok {
			t.Fatalf("save %d: ok=%v err=%v", i, ok, err)
		}
		ids = append(ids, m.ID)
	}
	if ok, _ := s.Messages.Save(ctx, &domain.Message{ConnectionID: "c1", ChatID: 42, MessageID: 1, Text: "dup", SentAt: now}); ok {
		t.Error("duplicate message must be ignored")
	}
	if err := s.Messages.UpdateText(ctx, "c1", 42, 2, "edited"); err != nil {
		t.Fatal(err)
	}
	if err := s.Messages.MarkDeleted(ctx, "c1", 42, []int{3}); err != nil {
		t.Fatal(err)
	}

	pending, err := s.Messages.Pending(ctx)
	if err != nil || len(pending) != 2 || pending[1].Text != "edited" {
		t.Fatalf("pending: %+v err=%v", pending, err)
	}
	hist, err := s.Messages.History(ctx, "c1", 42, ids[2], 10)
	if err != nil || len(hist) != 2 || hist[0].MessageID != 1 {
		t.Fatalf("history order/filter: %+v err=%v", hist, err)
	}
	if err := s.Messages.MarkAnalyzed(ctx, ids, 7); err != nil {
		t.Fatal(err)
	}
	if pending, _ = s.Messages.Pending(ctx); len(pending) != 0 {
		t.Errorf("expected no pending, got %d", len(pending))
	}
}

func TestTasksListAndSnooze(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	deadline := time.Now().Add(-time.Hour)

	mk := func(title string, p domain.Priority, st domain.TaskStatus) *domain.Task {
		task := &domain.Task{ConnectionID: "c1", ChatID: 42, SenderID: 42, SenderName: "Пётр", Title: title,
			Priority: p, Category: domain.CategoryBug, Status: st, SourceMessageIDs: []int{10, 11}}
		if err := s.Tasks.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	low := mk("low", domain.PriorityLow, domain.StatusNew)
	crit := mk("crit", domain.PriorityCritical, domain.StatusInProgress)
	mk("done", domain.PriorityHigh, domain.StatusDone)

	crit.Deadline = &deadline
	if err := s.Tasks.Update(ctx, crit); err != nil {
		t.Fatal(err)
	}

	list, total, err := s.Tasks.List(ctx, domain.TaskFilter{
		Statuses: []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress}, Limit: 10,
	})
	if err != nil || total != 2 || list[0].Title != "crit" {
		t.Fatalf("list: total=%d %+v err=%v", total, list, err)
	}
	if got, _ := s.Tasks.Get(ctx, crit.ID); len(got.SourceMessageIDs) != 2 || got.Deadline == nil {
		t.Errorf("roundtrip lost fields: %+v", got)
	}
	if n, _ := s.Tasks.CountOverdue(ctx, domain.ScopeAll, time.Now()); n != 1 {
		t.Errorf("overdue: %d", n)
	}

	past := time.Now().Add(-time.Minute)
	low.Status, low.PrevStatus, low.SnoozeUntil = domain.StatusSnoozed, domain.StatusNew, &past
	if err := s.Tasks.Update(ctx, low); err != nil {
		t.Fatal(err)
	}
	due, err := s.Tasks.DueSnoozed(ctx, time.Now())
	if err != nil || len(due) != 1 || due[0].ID != low.ID {
		t.Fatalf("due snoozed: %+v err=%v", due, err)
	}
	if _, err := s.Tasks.Get(ctx, 9999); err != domain.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSettingsAndConnections(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.Settings.SetMany(ctx, map[string]string{"ai.provider": "gemini", "meta.last_digest": "2026-09-14"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings.DeleteExceptPrefix(ctx, "meta."); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Settings.All(ctx)
	if len(all) != 1 || all["meta.last_digest"] != "2026-09-14" {
		t.Errorf("reset must keep meta keys only: %v", all)
	}

	c := &domain.BusinessConnection{ID: "c1", UserID: 1, UserChatID: 1, UserName: "Owner", CanReply: true, Enabled: true}
	if err := s.Connections.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.CanReply = false
	if err := s.Connections.Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Connections.LatestForUser(ctx, 1)
	if err != nil || got.CanReply || !got.Enabled {
		t.Errorf("connection upsert: %+v err=%v", got, err)
	}
}
