package service

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, loc) // Monday

	cases := map[string]string{
		"90":           "2026-09-14 13:30",
		"30m":          "2026-09-14 12:30",
		"2h":           "2026-09-14 14:00",
		"1d2h30m":      "2026-09-15 14:30",
		"2ч":           "2026-09-14 14:00",
		"45 мин":       "2026-09-14 12:45",
		"3д":           "2026-09-17 12:00",
		"18:00":        "2026-09-14 18:00",
		"09:00":        "2026-09-15 09:00",
		"завтра":       "2026-09-15 09:00",
		"Завтра 10:30": "2026-09-15 10:30",
		"20.09 12:00":  "2026-09-20 12:00",
		"01.02":        "2027-02-01 09:00",
		"15.10.2026":   "2026-10-15 09:00",
	}
	for in, want := range cases {
		got, err := ParseWhen(in, now, loc)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if s := got.Format("2006-01-02 15:04"); s != want {
			t.Errorf("%q: got %s, want %s", in, s, want)
		}
	}

	for _, bad := range []string{"", "когда-нибудь", "0", "-5", "10.09.2026 10:00", "25:00", "400d"} {
		if _, err := ParseWhen(bad, now, loc); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseClock(t *testing.T) {
	if h, m, err := ParseClock(" 08:05 "); err != nil || h != 8 || m != 5 {
		t.Errorf("got %d:%d %v", h, m, err)
	}
	for _, bad := range []string{"8", "24:00", "12:60", "aa:bb"} {
		if _, _, err := ParseClock(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
