package service

import (
	"strings"
	"testing"

	"tgtriage/internal/domain"
)

func TestSpreadRepliesCapsEachChat(t *testing.T) {
	var msgs []domain.Message // newest first
	for i := 0; i < 10; i++ {
		msgs = append(msgs, domain.Message{ChatID: 1, Text: "a" + string(rune('0'+i))})
	}
	msgs = append(msgs, domain.Message{ChatID: 2, Text: "b0"}, domain.Message{ChatID: 2, Text: "b0"})
	got := spreadReplies(msgs, 100, 3)
	if want := "b0 a2 a1 a0"; strings.Join(got, " ") != want {
		t.Errorf("want %q (3 newest of chat 1, chat 2 deduplicated, oldest first), got %v", want, got)
	}
}
