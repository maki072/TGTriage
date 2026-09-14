package service

import (
	"strings"
	"unicode"

	"tgtriage/internal/domain"
)

// noisePhrases are exact-match (after trim/lowercase/punctuation-strip), context-independent
// closers — things people only ever send to acknowledge, never to raise something new.
// Deliberately conservative: bare "да"/"нет"/"ок?" and the like are NOT here, because their
// meaning depends entirely on what they're replying to, and a heuristic can't see that context —
// skipping the LLM for those risks silently losing a real task. Only phrases that are noise in
// essentially every context make the list. Adjust freely; this is a plain map, not a model.
var noisePhrases = map[string]bool{
	"спасибо": true, "спасибо!": true, "спасибо большое": true, "спасибо огромное": true,
	"спс": true, "благодарю": true, "пасиб": true, "пасиба": true, "пасибо": true, "спасибочки": true,
	"понял": true, "поняла": true, "поняли": true, "принято": true, "принял": true, "принял, спасибо": true,
	"ясно": true, "ясно, спасибо": true, "хорошо, спасибо": true, "ок, спасибо": true, "окей, спасибо": true,
	"супер": true, "круто": true, "отлично": true, "класс": true, "здорово": true, "прекрасно": true,
	"договорились": true, "заметано": true, "по рукам": true, "не за что": true, "пожалуйста": true,
	"thanks": true, "thank you": true, "thanks!": true, "thx": true, "ty": true, "tysm": true,
	"got it": true, "gotcha": true, "sounds good": true, "no problem": true, "np": true,
	"cool": true, "great": true, "nice": true, "perfect": true, "awesome": true,
}

// isHeuristicNoise reports whether every message in the batch is unambiguous noise — safe to
// skip the LLM call for entirely. Deliberately strict: if even one message doesn't match, the
// whole batch still goes to the model, so a noise phrase glued to a real request is never lost.
func isHeuristicNoise(msgs []domain.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	for _, m := range msgs {
		if !isNoiseText(m.Text) {
			return false
		}
	}
	return true
}

func isNoiseText(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.TrimRight(t, "!.,;: ")
	if t == "" {
		return false // empty or media-only (photo/voice/etc.) messages are never auto-noise
	}
	return noisePhrases[t] || isEmojiOnly(t)
}

// isEmojiOnly reports whether s consists solely of emoji/whitespace — a pure reaction like "👍" or "🙏😊".
func isEmojiOnly(s string) bool {
	sawEmoji := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			continue
		case isEmojiRune(r):
			sawEmoji = true
		default:
			return false
		}
	}
	return sawEmoji
}

func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // pictographs, emoticons, transport, supplemental symbols
		return true
	case r >= 0x2600 && r <= 0x27BF: // misc symbols & dingbats (❤ lives here; 👍 is in the block above)
		return true
	case r == 0xFE0F || r == 0x200D: // variation selector, zero-width joiner (emoji sequences)
		return true
	default:
		return false
	}
}
