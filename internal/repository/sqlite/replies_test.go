package sqlite

import (
	"context"
	"testing"
	"time"

	"tgtriage/internal/domain"
)

func TestOwnerRepliesKeepsOnlyWhatTheOwnerTyped(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now()
	save := func(conn string, chat int64, id int, out, viaBot bool, text string) {
		t.Helper()
		if _, err := s.Messages.Save(ctx, &domain.Message{ConnectionID: conn, ChatID: chat, MessageID: id,
			Outgoing: out, ViaBot: viaBot, Text: text, SentAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	save("c1", 1, 1, true, false, "Привет, гляну сегодня")
	save("c1", 1, 2, true, true, "Черновик, отправленный ботом")
	save("c1", 1, 3, false, false, "Сообщение собеседника")
	save("c1", 1, 4, true, false, "ок")                              // too short to say anything about style
	save("helpdesk", 1, 5, true, false, "Ответ оператора поддержки") // support desk, not the owner
	save("c1", 2, 6, true, false, "Другой чат, тут я пишу иначе")

	all, err := s.Messages.OwnerReplies(ctx, domain.ReplyQuery{Limit: 10})
	if err != nil || len(all) != 2 {
		t.Fatalf("all chats: %+v err=%v", all, err)
	}
	one, _ := s.Messages.OwnerReplies(ctx, domain.ReplyQuery{ConnectionID: "c1", ChatID: 1, Limit: 10})
	if len(one) != 1 || one[0].Text != "Привет, гляну сегодня" {
		t.Errorf("one chat: %+v", one)
	}
	other, _ := s.Messages.OwnerReplies(ctx, domain.ReplyQuery{ExceptChatID: 1, Limit: 10})
	if len(other) != 1 || other[0].ChatID != 2 {
		t.Errorf("except chat: %+v", other)
	}
	top, err := s.Messages.TopReplyChats(ctx, 1, 5)
	if err != nil || len(top) != 2 {
		t.Errorf("top chats: %+v err=%v", top, err)
	}
}
