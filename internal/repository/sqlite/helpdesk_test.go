package sqlite

import (
	"context"
	"testing"
	"time"

	"tgtriage/internal/domain"
)

func TestHelpdeskRepo(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	r := s.Helpdesk

	old := time.Now().Add(-30 * time.Minute)
	waiting := &domain.HelpdeskUser{UserID: 10, Name: "Иван", Username: "ivan", GroupID: -1001, TopicID: 55, AwaitingSince: &old}
	idle := &domain.HelpdeskUser{UserID: 11, Name: "Пётр", GroupID: -1001, TopicID: 56}
	for _, u := range []*domain.HelpdeskUser{waiting, idle} {
		if err := r.SaveUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	waiting.Source = "site"
	if err := r.SaveUser(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	got, err := r.UserByTopic(ctx, 0, -1001, 55)
	if err != nil || got.UserID != 10 || got.Source != "site" || got.AwaitingSince == nil {
		t.Fatalf("user by topic: %+v err=%v", got, err)
	}
	if _, err := r.UserByTopic(ctx, 0, -1001, 999); err != domain.ErrNotFound {
		t.Errorf("unknown topic: expected ErrNotFound, got %v", err)
	}

	list, total, err := r.ListUsers(ctx, nil, domain.HelpdeskUserFilter{Limit: 10})
	if err != nil || total != 2 || list[0].UserID != 10 {
		t.Fatalf("waiting users must come first: total=%d %+v err=%v", total, list, err)
	}
	if _, n, _ := r.ListUsers(ctx, nil, domain.HelpdeskUserFilter{AwaitingOnly: true}); n != 1 {
		t.Errorf("awaiting filter: %d", n)
	}
	if _, n, _ := r.ListUsers(ctx, nil, domain.HelpdeskUserFilter{Query: "@ivan"}); n != 0 {
		t.Errorf("query by @username must not match LIKE with @: %d", n)
	}
	if _, n, _ := r.ListUsers(ctx, nil, domain.HelpdeskUserFilter{Query: "Пётр"}); n != 1 {
		t.Errorf("query by name: %d", n)
	}

	due, err := r.DueReminders(ctx, 0, time.Now().Add(-15*time.Minute))
	if err != nil || len(due) != 1 || due[0].UserID != 10 {
		t.Fatalf("due reminders: %+v err=%v", due, err)
	}
	now := time.Now()
	waiting.RemindedAt = &now
	_ = r.SaveUser(ctx, waiting)
	if due, _ := r.DueReminders(ctx, 0, time.Now().Add(-15*time.Minute)); len(due) != 0 {
		t.Errorf("just reminded user must not be due again: %+v", due)
	}

	in := &domain.HelpdeskMessage{UserID: 10, Direction: domain.HelpdeskIn, UserMsgID: 5, GroupID: -1001, GroupMsgID: 900}
	out := &domain.HelpdeskMessage{UserID: 10, Direction: domain.HelpdeskOut, UserMsgID: 6, GroupID: -1001, GroupMsgID: 901}
	for _, m := range []*domain.HelpdeskMessage{in, out} {
		if err := r.SaveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := r.MessageByUserMsg(ctx, 0, 10, 5); err != nil || m.GroupMsgID != 900 {
		t.Errorf("by user msg: %+v err=%v", m, err)
	}
	if m, err := r.MessageByGroupMsg(ctx, 0, -1001, 901); err != nil || m.Direction != domain.HelpdeskOut || m.UserMsgID != 6 {
		t.Errorf("by group msg: %+v err=%v", m, err)
	}
	if around, _ := r.MessagesAround(ctx, 0, -1001, time.Now(), 3*time.Second); len(around) != 1 || around[0].UserMsgID != 5 {
		t.Errorf("messages around must return incoming copies only: %+v", around)
	}

	if err := r.SaveCard(ctx, domain.HelpdeskCard{TaskID: 1, ChatID: -1001, TopicID: 55, MessageID: 950}); err != nil {
		t.Fatal(err)
	}
	_ = r.SaveCard(ctx, domain.HelpdeskCard{TaskID: 1, ChatID: -1001, TopicID: 55, MessageID: 950}) // duplicate ignored
	if cards, _ := r.Cards(ctx, 1); len(cards) != 1 || cards[0].TopicID != 55 {
		t.Errorf("cards: %+v", cards)
	}

	if err := s.Backup(ctx, t.TempDir()+"/copy.db"); err != nil {
		t.Errorf("backup: %v", err)
	}
}
