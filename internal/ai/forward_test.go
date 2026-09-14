package ai

import (
	"strings"
	"testing"
	"time"

	"tgtriage/internal/domain"
)

func TestForwardUserPrompt(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	in := ForwardInput{
		Now:      time.Date(2026, 9, 14, 12, 0, 0, 0, loc),
		Location: loc,
		Messages: []domain.Message{
			{SenderName: "Иван Петров", SenderUsername: "ivan", Text: "Скинь отчёт до пятницы", SentAt: time.Date(2026, 9, 10, 9, 30, 0, 0, loc)},
			{SenderName: "Я", Outgoing: true, Text: "Ок, сделаю", SentAt: time.Date(2026, 9, 10, 9, 31, 0, 0, loc)},
		},
	}
	got := ForwardUserPrompt(in)
	for _, want := range []string{
		"CURRENT_TIME: 2026-09-14 12:00 (понедельник)",
		"[10.09.2026 09:30] Иван Петров (@ivan): Скинь отчёт до пятницы\n",
		"[10.09.2026 09:31] OWNER: Ок, сделаю\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestForwardSystemPromptPlaceholders(t *testing.T) {
	got := ForwardSystemPrompt(ForwardInput{OwnerName: "Мария", OwnerAbout: "  тимлид  "})
	if strings.Contains(got, "{{") {
		t.Errorf("unreplaced placeholder:\n%s", got)
	}
	if !strings.Contains(got, "(Мария)") || !strings.Contains(got, "О владельце: тимлид") {
		t.Errorf("owner fields not rendered:\n%s", got)
	}
}
