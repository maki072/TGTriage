package ai

import (
	"regexp"
	"strings"
	"unicode"
)

// Mask replaces a secret in text that is sent to an LLM provider. Logins, passwords, tokens, keys and
// remote-access codes of the owner's contacts never leave the server: the model only needs to know
// that "some credential" was mentioned, not what it is.
const Mask = "[скрыто]"

// secretLabels are words that announce a credential. Used with an explicit separator ("пароль: x",
// "token=x", "логин - x"), so ordinary sentences with these words are left alone.
const secretLabels = `пароль|пароля|паролю|паролем|парол[ьи]|пасс?ворд|пасс|password|passwd|passphrase|pass|pwd|` +
	`логин|логина|логину|логином|login|username|юзернейм|` +
	`токен|token|secret|секрет|api[ _-]?key|apikey|access[ _-]?key|private[ _-]?key|` +
	`учётка|учетка|креды|creds|credentials`

// strongLabels also redact a value after plain whitespace ("пароль Qwerty123"), but only when the
// value looks like a credential.
const strongLabels = `пароль|пароля|паролю|паролем|парол[ьи]|пасс?ворд|password|passwd|pwd|токен|token`

// loginLabels redact the next word after whitespace unless it is a common word.
const loginLabels = `логин|логина|логину|логином|login|username`

// remoteLabels announce a remote-access id or code (AnyDesk, TeamViewer...) or a one-time code.
const remoteLabels = `anydesk|any desk|энидеск|анидеск|анидэск|teamviewer|team viewer|тимвьювер|тим вьювер|` +
	`rustdesk|ammyy|аммии|радмин|radmin|rdp|айди|id|код\p{L}*|code`

// pre is a word boundary that also works for Cyrillic (RE2's \b knows only ASCII letters). It is group 1
// of the label patterns and is put back as it was.
const pre = `(^|[^\p{L}\p{N}_])`

var (
	rePrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
	reURLCreds   = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/:@]+:[^\s/@]+@`)
	reURLSecret  = regexp.MustCompile(`(?i)([?&](?:token|access_token|api_key|apikey|key|password|pwd|pass|secret|sig|signature|auth)=)[^&\s]+`)

	// well-known credential formats
	reKnownTokens = regexp.MustCompile(`\b(?:` +
		`sk-[A-Za-z0-9_-]{16,}|` + // OpenAI / Anthropic / OpenRouter style
		`gsk_[A-Za-z0-9]{20,}|` + // Groq
		`AIza[0-9A-Za-z_-]{30,}|` + // Google
		`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|` + // GitHub
		`xox[abprs]-[A-Za-z0-9-]{10,}|` + // Slack
		`(?:AKIA|ASIA)[A-Z0-9]{16}|` + // AWS
		`\d{8,10}:[A-Za-z0-9_-]{35}|` + // Telegram bot token
		`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}` + // JWT
		`)\b`)
	reBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{16,}`)

	reLabelSep = regexp.MustCompile(`(?i)` + pre + `((?:` + secretLabels + `)[ \t]*(?:[:=]|[-–—]{1,2})[ \t]*(?:\r?\n[ \t]*)?)("[^"\n]*"|'[^'\n]*'|«[^»\n]*»|\S+)`)
	reWeak     = regexp.MustCompile(`(?i)` + pre + `((?:ключ|key|пин|pin|otp)[ \t]*[:=][ \t]*)(\S+)`)
	reStrong   = regexp.MustCompile(`(?i)` + pre + `((?:` + strongLabels + `)[ \t]+(?:(?:is|это)[ \t]+)?)(\S{4,})`)
	reLogin    = regexp.MustCompile(`(?i)` + pre + `((?:` + loginLabels + `)[ \t]+(?:(?:is|это)[ \t]+)?)([^\s,;.]{2,})`)
	reRemote   = regexp.MustCompile(`(?i)` + pre + `((?:` + remoteLabels + `)[^\p{L}\p{N}_\n\d][^\n\d]{0,24})((?:\d[ -]?){5,11}\d)`)

	reEmail    = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*`)
	reCardLike = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
	reLongTok  = regexp.MustCompile(`[A-Za-z0-9_+=]{32,}`)                     // no "/" and "-": URL paths and slugs are not secrets
	reProdKey  = regexp.MustCompile(`\b[A-Z0-9]{4,6}(?:-[A-Z0-9]{4,6}){3,}\b`) // license / product keys
)

// loginStop are ordinary words that may follow "логин" without being a login.
var loginStop = map[string]bool{
	"не": true, "и": true, "или": true, "от": true, "для": true, "на": true, "в": true, "с": true, "к": true,
	"по": true, "был": true, "была": true, "это": true, "уже": true, "там": true, "тут": true, "у": true,
	"же": true, "а": true, "но": true, "как": true, "что": true, "то": true, "да": true, "нет": true,
	"сейчас": true, "тоже": true, "пока": true, "такой": true, "правильный": true, "верный": true,
	"неверный": true, "пользователя": true, "и/или": true, "is": true, "and": true, "or": true, "the": true,
	"not": true, "failed": true, "error": true, "page": true, "form": true,
}

// Redact hides credentials in text bound for an LLM provider. It is applied to every prompt right before
// the request leaves the server, so a new prompt builder cannot forget it. It is idempotent.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = rePrivateKey.ReplaceAllString(s, Mask)
	s = reURLCreds.ReplaceAllString(s, "${1}"+Mask+"@")
	s = reURLSecret.ReplaceAllString(s, "${1}"+Mask)
	s = reKnownTokens.ReplaceAllString(s, Mask)
	s = reBearer.ReplaceAllString(s, "${1} "+Mask)
	s = reLabelSep.ReplaceAllString(s, "${1}${2}"+Mask)
	s = reWeak.ReplaceAllString(s, "${1}${2}"+Mask)
	s = reStrong.ReplaceAllStringFunc(s, func(m string) string {
		sub := reStrong.FindStringSubmatch(m)
		if looksLikeSecret(sub[3]) {
			return sub[1] + sub[2] + Mask
		}
		return m
	})
	s = reLogin.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLogin.FindStringSubmatch(m)
		if loginStop[strings.ToLower(sub[3])] || strings.HasPrefix(sub[3], "[") {
			return m
		}
		return sub[1] + sub[2] + Mask
	})
	s = reRemote.ReplaceAllString(s, "${1}${2}"+Mask)
	s = reProdKey.ReplaceAllStringFunc(s, func(m string) string {
		if hasLetter(m) && hasDigit(m) {
			return Mask
		}
		return m
	})
	s = reEmail.ReplaceAllString(s, "[email]")
	s = reCardLike.ReplaceAllStringFunc(s, func(m string) string {
		if luhnValid(m) {
			return "[карта]"
		}
		return m
	})
	s = reLongTok.ReplaceAllStringFunc(s, func(m string) string {
		if hasLetter(m) && hasDigit(m) {
			return Mask
		}
		return m
	})
	return s
}

// RedactMessage is Redact for one chat message. On top of the patterns it catches the typical way a
// password is sent in a chat — alone, as the whole message ("Qwerty123!").
func RedactMessage(s string) string {
	if t := strings.TrimSpace(s); looksLikeBareSecret(t) {
		return Mask
	}
	return Redact(s)
}

// looksLikeBareSecret: a single word of 8-64 characters with a digit and at least three of the four
// classes (lower, upper, digit, symbol) — a password, not a word, a path or a link. Trailing punctuation
// is ignored, so "Спасибо!" is not a password.
func looksLikeBareSecret(t string) bool {
	t = strings.TrimRight(t, ".,;:!?)(")
	n := len([]rune(t))
	if n < 8 || n > 64 || strings.ContainsAny(t, " \t\r\n/\\") {
		return false
	}
	var lower, upper, digit, symbol bool
	for _, r := range t {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	classes := 0
	for _, c := range []bool{lower, upper, digit, symbol} {
		if c {
			classes++
		}
	}
	return classes >= 3 && digit && (lower || upper)
}

// looksLikeSecret is the value check for "пароль <value>" without a separator: a digit or a symbol, or
// a mix of cases, so that "пароль не подошёл" is left alone.
func looksLikeSecret(v string) bool {
	var lower, upper bool
	for _, r := range v {
		switch {
		case unicode.IsDigit(r):
			return true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case r != '-' && r != '_' && r != '.':
			return true
		}
	}
	return lower && upper
}

func hasLetter(s string) bool { return strings.IndexFunc(s, unicode.IsLetter) >= 0 }
func hasDigit(s string) bool  { return strings.IndexFunc(s, unicode.IsDigit) >= 0 }

func luhnValid(s string) bool {
	sum, double, digits := 0, false, 0
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
		digits++
	}
	return digits >= 13 && sum%10 == 0
}
