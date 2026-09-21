package webapp

import (
	"testing"
	"time"

	"tgtriage/internal/domain"
)

func TestForOperatorHidesTriageInternals(t *testing.T) {
	at := time.Now()
	dto := toTaskDTO(domain.Task{ID: 3, Title: "Не приходит код", Provider: "claude", Model: "m", Confidence: 0.9,
		Importance: domain.PriorityHigh, RemindAt: &at, Priority: domain.PriorityHigh}, time.UTC)
	got := dto.forOperator()
	if got.Provider != "" || got.Model != "" || got.Confidence != 0 || got.Importance != "" || got.RemindAt != nil {
		t.Errorf("triage internals must not reach operators: %+v", got)
	}
	if got.Title != "Не приходит код" || got.Priority != "high" || got.ID != 3 {
		t.Errorf("the ticket itself must stay intact: %+v", got)
	}
	if dto.Provider == "" {
		t.Error("forOperator must work on a copy")
	}
}
