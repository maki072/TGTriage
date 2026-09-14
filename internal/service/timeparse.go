package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

var durationRe = regexp.MustCompile(`^(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?$`)

const maxSnooze = 366 * 24 * time.Hour

// ParseWhen parses a user-supplied snooze target relative to now:
//   - "90" — minutes
//   - "30m", "2h", "1d", "1d2h30m", "2ч", "45мин", "3д" — durations
//   - "18:00" — today (or tomorrow if already passed)
//   - "завтра", "завтра 10:30", "tomorrow 10:30" — tomorrow (default 09:00)
//   - "15.09 18:00", "15.09.2026 18:00", "15.09" — absolute date (default 09:00)
func ParseWhen(input string, now time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	s := strings.ToLower(strings.Join(strings.Fields(input), " "))
	if s == "" {
		return time.Time{}, fmt.Errorf("%w: empty time", domain.ErrInvalidInput)
	}
	invalid := fmt.Errorf("%w: не удалось распознать время", domain.ErrInvalidInput)

	check := func(t time.Time) (time.Time, error) {
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("%w: время уже прошло", domain.ErrInvalidInput)
		}
		if t.Sub(now) > maxSnooze {
			return time.Time{}, fmt.Errorf("%w: слишком далеко", domain.ErrInvalidInput)
		}
		return t, nil
	}

	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return time.Time{}, invalid
		}
		return check(now.Add(time.Duration(n) * time.Minute))
	}

	if d, ok := parseDuration(s); ok {
		return check(now.Add(d))
	}

	for _, prefix := range []string{"завтра", "tomorrow"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			h, m := 9, 0
			if rest = strings.TrimSpace(rest); rest != "" {
				var err error
				if h, m, err = ParseClock(strings.TrimPrefix(rest, "в ")); err != nil {
					return time.Time{}, invalid
				}
			}
			d := now.AddDate(0, 0, 1)
			return check(time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc))
		}
	}

	if h, m, err := ParseClock(s); err == nil {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return check(t)
	}

	type layout struct {
		format  string
		hasYear bool
		hasTime bool
	}
	for _, l := range []layout{
		{"02.01.2006 15:04", true, true},
		{"02.01 15:04", false, true},
		{"02.01.2006", true, false},
		{"02.01", false, false},
	} {
		t, err := time.ParseInLocation(l.format, s, loc)
		if err != nil {
			continue
		}
		h, m := 9, 0
		if l.hasTime {
			h, m = t.Hour(), t.Minute()
		}
		year := t.Year()
		if !l.hasYear {
			year = now.Year()
		}
		res := time.Date(year, t.Month(), t.Day(), h, m, 0, 0, loc)
		if !l.hasYear && !res.After(now) {
			res = res.AddDate(1, 0, 0)
		}
		return check(res)
	}
	return time.Time{}, invalid
}

func parseDuration(s string) (time.Duration, bool) {
	s = strings.NewReplacer(" ", "", "дн", "d", "д", "d", "час", "h", "ч", "h", "мин", "m", "min", "m", "м", "m").Replace(s)
	g := durationRe.FindStringSubmatch(s)
	if g == nil || (g[1] == "" && g[2] == "" && g[3] == "") {
		return 0, false
	}
	var d time.Duration
	units := []time.Duration{24 * time.Hour, time.Hour, time.Minute}
	for i, u := range units {
		if g[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(g[i+1])
		if err != nil {
			return 0, false
		}
		d += time.Duration(n) * u
	}
	if d <= 0 {
		return 0, false
	}
	return d, true
}
