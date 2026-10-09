package sqlite

import (
	"context"
	"testing"

	"tgtriage/internal/domain"
)

func TestBotHelpdeskRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	b := &domain.Bot{Token: "1:abc", Label: "Org", Active: true, Helpdesk: domain.DefaultHelpdeskSettings()}
	if err := s.Bots.Save(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Bots.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Helpdesk.DeleteService || !got.Helpdesk.SpamScreen || got.Helpdesk.SpamCaptcha {
		t.Errorf("defaults lost: %+v", got.Helpdesk)
	}

	got.Helpdesk.DeleteService = false
	got.Helpdesk.SpamCaptcha = true
	if err := s.Bots.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.Bots.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Helpdesk.DeleteService || !again.Helpdesk.SpamCaptcha {
		t.Errorf("update lost: %+v", again.Helpdesk)
	}
}
