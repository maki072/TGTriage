package domain

import (
	"testing"
	"time"
)

func TestInWorkingHours(t *testing.T) {
	loc := time.UTC
	h := HelpdeskSettings{HoursEnabled: true, HoursStart: "09:00", HoursEnd: "18:00", HoursDays: "12345"}
	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, 9, 14, 9, 0, 0, 0, loc), true},   // Monday opening
		{time.Date(2026, 9, 14, 17, 59, 0, 0, loc), true}, // Monday before close
		{time.Date(2026, 9, 14, 18, 0, 0, 0, loc), false}, // closing time
		{time.Date(2026, 9, 19, 12, 0, 0, 0, loc), false}, // Saturday
	}
	for _, c := range cases {
		if got := h.InWorkingHours(c.at); got != c.want {
			t.Errorf("%s: got %v want %v", c.at, got, c.want)
		}
	}

	night := HelpdeskSettings{HoursEnabled: true, HoursStart: "22:00", HoursEnd: "06:00", HoursDays: "1"}
	if !night.InWorkingHours(time.Date(2026, 9, 14, 23, 0, 0, 0, loc)) {
		t.Error("Monday 23:00 is inside a Monday night shift")
	}
	if !night.InWorkingHours(time.Date(2026, 9, 15, 5, 0, 0, 0, loc)) {
		t.Error("Tuesday 05:00 belongs to the Monday night shift")
	}
	if night.InWorkingHours(time.Date(2026, 9, 16, 5, 0, 0, 0, loc)) {
		t.Error("Wednesday 05:00 belongs to a Tuesday shift, which is off")
	}

	if !(HelpdeskSettings{}).InWorkingHours(time.Date(2026, 9, 19, 3, 0, 0, 0, loc)) {
		t.Error("with working hours off every moment is working time")
	}
}

func TestHoursLabel(t *testing.T) {
	for days, want := range map[string]string{
		"12345":   "пн–пт 09:00–18:00",
		"1234567": "пн–вс 09:00–18:00",
		"135":     "пн, ср, пт 09:00–18:00",
		"1267":    "пн, вт, сб, вс 09:00–18:00",
	} {
		h := HelpdeskSettings{HoursStart: "09:00", HoursEnd: "18:00", HoursDays: days}
		if got := h.HoursLabel(); got != want {
			t.Errorf("%s: got %q want %q", days, got, want)
		}
	}
}

func TestTopicLink(t *testing.T) {
	if got := TopicLink(-1001234567890, 42); got != "https://t.me/c/1234567890/42" {
		t.Errorf("topic link: %s", got)
	}
}
