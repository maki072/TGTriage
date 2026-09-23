package ai

import (
	"strings"
	"testing"
	"time"
)

func TestUserPromptStyleBlock(t *testing.T) {
	in := TriageInput{Now: time.Now(), Style: Style{
		Profile: "коротко, на «ты»", ChatProfile: "шутит",
		Examples: []string{"ща гляну"}, OtherExamples: []string{"Добрый день, вернусь с ответом"},
	}}
	got := UserPrompt(in)
	for _, want := range []string{"<owner_style>", "коротко, на «ты»", "С этим собеседником: шутит", "- ща гляну", "- Добрый день, вернусь с ответом"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(UserPrompt(TriageInput{Now: time.Now()}), "owner_style") {
		t.Error("no style, no block")
	}
	in.Helpdesk = true
	if strings.Contains(UserPrompt(in), "owner_style") {
		t.Error("the support desk prompt must not carry the owner's style")
	}
}

func TestParseStyleProfile(t *testing.T) {
	if p, err := ParseStyleProfile("```json\n{\"profile\": \" коротко \"}\n```"); err != nil || p != "коротко" {
		t.Errorf("got %q err=%v", p, err)
	}
	for _, bad := range []string{"", "нет json", `{"profile": "  "}`} {
		if _, err := ParseStyleProfile(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
