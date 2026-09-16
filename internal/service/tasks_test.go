package service

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/repository/sqlite"
)

func newTaskServiceFixture(t *testing.T) (*TaskService, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := newTestSettings(t, newMemSettingsRepo(), nil)
	conns := NewConnectionService(store.Connections, 1)
	svc := NewTaskService(store.Tasks, store.Messages, store.Analyses, conns, settings, nil, slog.New(slog.DiscardHandler))
	return svc, store
}

func mkTask(t *testing.T, ctx context.Context, store *sqlite.Store, title string, p, imp domain.Priority, chatID int64, connectionID string) *domain.Task {
	t.Helper()
	task := &domain.Task{ConnectionID: connectionID, ChatID: chatID, SenderID: chatID, Title: title, Description: "desc " + title,
		SourceText: "src " + title, SourceMessageIDs: []int{1}, Priority: p, Importance: imp, Category: domain.CategoryTask, Status: domain.StatusNew}
	if err := store.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestTaskEdit(t *testing.T) {
	ctx := context.Background()
	svc, store := newTaskServiceFixture(t)
	task := mkTask(t, ctx, store, "orig", domain.PriorityLow, domain.PriorityLow, 42, "c1")

	got, err := svc.Edit(ctx, task.ID, EditInput{Title: " new title ", Description: " new desc ", Priority: domain.PriorityHigh, Importance: domain.PriorityCritical})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "new title" || got.Description != "new desc" || got.Priority != domain.PriorityHigh || got.Importance != domain.PriorityCritical {
		t.Fatalf("edit not applied: %+v", got)
	}

	if _, err := svc.Edit(ctx, task.ID, EditInput{Title: "  "}); err != domain.ErrInvalidInput {
		t.Errorf("blank title must be rejected, got %v", err)
	}
}

func TestTaskSetAndClearReminder(t *testing.T) {
	ctx := context.Background()
	svc, store := newTaskServiceFixture(t)
	task := mkTask(t, ctx, store, "t", domain.PriorityMedium, domain.PriorityMedium, 42, "c1")

	if _, err := svc.SetReminder(ctx, task.ID, time.Now().Add(-time.Minute)); err != domain.ErrInvalidInput {
		t.Errorf("past reminder must be rejected, got %v", err)
	}

	at := time.Now().Add(time.Hour)
	got, err := svc.SetReminder(ctx, task.ID, at)
	if err != nil || got.RemindAt == nil {
		t.Fatalf("set reminder: %+v err=%v", got, err)
	}

	fired, err := svc.WakeReminders(ctx)
	if err != nil || len(fired) != 0 {
		t.Fatalf("future reminder must not fire yet: %+v err=%v", fired, err)
	}

	got, err = svc.ClearReminder(ctx, task.ID)
	if err != nil || got.RemindAt != nil {
		t.Fatalf("clear reminder: %+v err=%v", got, err)
	}
}

func TestTaskMerge(t *testing.T) {
	ctx := context.Background()
	svc, store := newTaskServiceFixture(t)
	src := mkTask(t, ctx, store, "source", domain.PriorityLow, domain.PriorityCritical, 42, "c1")
	dst := mkTask(t, ctx, store, "target", domain.PriorityMedium, domain.PriorityLow, 42, "c1")

	merged, err := svc.Merge(ctx, src.ID, dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if merged.ID != dst.ID {
		t.Fatalf("merge must return the target task, got #%d", merged.ID)
	}
	// the more urgent/important of the two wins (lower Priority.Rank() = more urgent)
	if merged.Priority != domain.PriorityMedium {
		t.Errorf("expected merged priority=medium (more urgent than source's low), got %v", merged.Priority)
	}
	if merged.Importance != domain.PriorityCritical {
		t.Errorf("expected merged importance=critical, got %v", merged.Importance)
	}
	if len(merged.SourceMessageIDs) != 2 {
		t.Errorf("source message ids not merged: %+v", merged.SourceMessageIDs)
	}
	if merged.SourceText == "" || merged.Description == "" {
		t.Errorf("text not folded into target: %+v", merged)
	}

	gotSrc, err := store.Tasks.Get(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSrc.MergedInto != dst.ID || gotSrc.Status != domain.StatusDone || gotSrc.ClosedAt == nil {
		t.Errorf("source task not closed/marked merged: %+v", gotSrc)
	}
	if !gotSrc.IsMerged() {
		t.Error("IsMerged() must report true for the source task")
	}

	if _, err := svc.Merge(ctx, src.ID, dst.ID); err != domain.ErrInvalidInput {
		t.Errorf("merging an already-merged task must fail, got %v", err)
	}
	if _, err := svc.Merge(ctx, dst.ID, dst.ID); err != domain.ErrInvalidInput {
		t.Errorf("merging a task with itself must fail, got %v", err)
	}
}

func TestNudgePersonal(t *testing.T) {
	ctx := context.Background()
	svc, store := newTaskServiceFixture(t)

	// backdated so it's immediately due for a nudge: Create() only stamps CreatedAt when it's zero
	personal := &domain.Task{ConnectionID: "c1", ChatID: 42, SenderID: 42, Title: "personal", Priority: domain.PriorityMedium,
		Importance: domain.PriorityMedium, Category: domain.CategoryTask, Status: domain.StatusNew, CreatedAt: time.Now().Add(-time.Hour)}
	if err := store.Tasks.Create(ctx, personal); err != nil {
		t.Fatal(err)
	}
	mkTask(t, ctx, store, "forwarded", domain.PriorityMedium, domain.PriorityMedium, 0, "c1")
	mkTask(t, ctx, store, "ticket", domain.PriorityMedium, domain.PriorityMedium, 7, domain.HelpdeskConnectionID)

	if nudged, err := svc.NudgePersonal(ctx, 0); err != nil || nudged != nil {
		t.Fatalf("minutes<=0 must disable nudges, got %+v err=%v", nudged, err)
	}

	nudged, err := svc.NudgePersonal(ctx, 30)
	if err != nil || len(nudged) != 1 || nudged[0].ID != personal.ID {
		t.Fatalf("expected personal task to be nudged: %+v err=%v", nudged, err)
	}
	// a second call right away must not re-nudge (last_reminded_at was just stamped)
	if nudged, err = svc.NudgePersonal(ctx, 30); err != nil || len(nudged) != 0 {
		t.Fatalf("must not double-nudge immediately: %+v err=%v", nudged, err)
	}
}
