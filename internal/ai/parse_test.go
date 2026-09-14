package ai

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tgtriage/internal/domain"
)

func TestParseAnalysisFencedAndNormalized(t *testing.T) {
	raw := "Вот результат:\n```json\n" + `{
		"message_type": "bug", "analysis": "Сообщение о падении", "is_task": true, "confidence": 1.7,
		"update_task_id": -3, "title": "", "description": "Упал логин в админку. Нужно починить.",
		"priority": "URGENT", "category": "Bug", "deadline": "2026-09-15T18:00",
		"reply_strategy": "maybe", "draft_reply": "Принял, посмотрю {скобки} \"кавычки\""
	}` + "\n```"
	a, err := ParseAnalysis(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Confidence != 1 {
		t.Errorf("confidence not clamped: %v", a.Confidence)
	}
	if a.UpdateTaskID != 0 {
		t.Errorf("negative update_task_id not reset: %d", a.UpdateTaskID)
	}
	if a.Priority != string(domain.PriorityMedium) {
		t.Errorf("unknown priority should become medium, got %q", a.Priority)
	}
	if a.Category != string(domain.CategoryBug) {
		t.Errorf("category not normalized: %q", a.Category)
	}
	if a.ReplyStrategy != "none" {
		t.Errorf("invalid strategy not normalized: %q", a.ReplyStrategy)
	}
	if a.Title != "Упал логин в админку" {
		t.Errorf("title fallback from description failed: %q", a.Title)
	}
	if !strings.Contains(a.DraftReply, "{скобки}") {
		t.Errorf("braces inside strings broke extraction: %q", a.DraftReply)
	}
}

func TestParseAnalysisNoiseClearsDraft(t *testing.T) {
	a, err := ParseAnalysis(`{"message_type":"gratitude","analysis":"спасибо","is_task":false,"confidence":0.1,
		"update_task_id":5,"title":"","description":"","priority":"low","category":"other","deadline":"",
		"reply_strategy":"none","draft_reply":"Пожалуйста!"}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.DraftReply != "" || a.UpdateTaskID != 0 {
		t.Errorf("noise must not carry draft/update: %+v", a)
	}
}

func TestParseAnalysisErrors(t *testing.T) {
	for _, in := range []string{"", "no json here", `{"is_task": true}`, `{"is_task": "yes"}`} {
		if _, err := ParseAnalysis(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestParseDeadline(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	cases := map[string]string{
		"2026-09-15T18:00":          "2026-09-15 18:00",
		"2026-09-15 09:30":          "2026-09-15 09:30",
		"2026-09-15":                "2026-09-15 23:59",
		"2026-09-15T15:00:00Z":      "2026-09-15 18:00",
		"2026-09-15T18:00:00+03:00": "2026-09-15 18:00",
	}
	for in, want := range cases {
		got := ParseDeadline(in, loc)
		if got == nil {
			t.Errorf("%q: got nil", in)
			continue
		}
		if s := got.In(loc).Format("2006-01-02 15:04"); s != want {
			t.Errorf("%q: got %s, want %s", in, s, want)
		}
	}
	if ParseDeadline("", loc) != nil || ParseDeadline("в пятницу", loc) != nil {
		t.Error("empty/invalid deadline must be nil")
	}
}

func TestSchemaAndPrompt(t *testing.T) {
	schema := AnalysisSchema()
	props := schema["properties"].(map[string]any)
	required := schema["required"].([]string)
	if len(props) != len(required) {
		t.Fatalf("every property must be required: %d props, %d required", len(props), len(required))
	}
	// schema field names must match domain.Analysis JSON tags
	blob, _ := json.Marshal(domain.Analysis{})
	var tags map[string]any
	_ = json.Unmarshal(blob, &tags)
	for _, name := range required {
		if _, ok := tags[name]; !ok {
			t.Errorf("schema field %q is missing in domain.Analysis", name)
		}
	}

	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	in := TriageInput{
		OwnerName: "Иван", Now: now, Sensitivity: domain.SensitivityHigh, ContactName: "Пётр",
		Context: []domain.Message{{Text: "привет", Outgoing: true, SentAt: now}},
		New:     []domain.Message{{Text: "глянь баг", SentAt: now}},
	}
	sys := SystemPrompt(in)
	if strings.Contains(sys, "{{") || !strings.Contains(sys, "ВЫСОКАЯ") || !strings.Contains(sys, "draft_reply") {
		t.Error("system prompt placeholders not rendered")
	}
	// The JSON schema is enforced structurally (output_config.format / responseJsonSchema /
	// response_format json_schema) and deliberately not duplicated as text in the prompt anymore.
	if strings.Contains(sys, `"additionalProperties"`) {
		t.Error("system prompt should not embed the raw JSON schema — it costs tokens for no benefit")
	}
	user := UserPrompt(in)
	if !strings.Contains(user, "OWNER: привет") || !strings.Contains(user, "CONTACT: глянь баг") {
		t.Errorf("user prompt malformed:\n%s", user)
	}
}
