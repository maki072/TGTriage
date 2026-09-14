package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// ParseAnalysis extracts and validates the JSON analysis from a model answer.
// It tolerates markdown fences and surrounding text as a defense in depth.
func ParseAnalysis(text string) (*domain.Analysis, error) {
	raw := extractJSONObject(text)
	if raw == "" {
		return nil, errors.New("no JSON object in model response")
	}
	var a domain.Analysis
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("decode analysis JSON: %w", err)
	}
	normalize(&a)
	if a.IsTask && a.Title == "" {
		if a.Description == "" {
			return nil, errors.New("analysis marked as task but has no title and description")
		}
		a.Title = truncateRunes(firstSentence(a.Description), 80)
	}
	return &a, nil
}

func normalize(a *domain.Analysis) {
	a.Title = strings.TrimSpace(a.Title)
	a.Description = strings.TrimSpace(a.Description)
	a.DraftReply = strings.TrimSpace(a.DraftReply)
	a.Deadline = strings.TrimSpace(a.Deadline)
	a.Reasoning = strings.TrimSpace(a.Reasoning)
	if math.IsNaN(a.Confidence) {
		a.Confidence = 0
	}
	a.Confidence = math.Max(0, math.Min(1, a.Confidence))
	a.Priority = string(domain.ParsePriority(a.Priority))
	a.Category = string(domain.ParseCategory(a.Category))
	switch a.ReplyStrategy {
	case "confirm", "clarify", "decline", "none":
	default:
		a.ReplyStrategy = "none"
	}
	if a.UpdateTaskID < 0 {
		a.UpdateTaskID = 0
	}
	a.Title = truncateRunes(a.Title, 150)
	if !a.IsTask {
		a.DraftReply = ""
		a.UpdateTaskID = 0
	}
}

func firstSentence(s string) string {
	if i := strings.IndexAny(s, ".!?\n"); i > 0 {
		return s[:i]
	}
	return s
}

// extractJSONObject returns the first balanced top-level JSON object in s.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr, esc := false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// ParseDeadline converts the model's deadline string into a time in loc. Returns nil if empty/invalid.
func ParseDeadline(s string, loc *time.Location) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		t = t.In(loc)
		return &t
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return &t
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		t = t.Add(23*time.Hour + 59*time.Minute)
		return &t
	}
	return nil
}
