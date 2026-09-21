package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"

	"tgtriage/internal/domain"
)

// Anti-spam for new users. A user stays unverified until they pass the captcha, an operator
// approves them or answers them. While unverified, a message that looks like spam — or any message,
// when the captcha is on — is held instead of relayed: it waits in hd_held until the user presses
// "I am not a bot" or an operator decides in the quarantine topic (or the panel).

const (
	maxHeldPerUser  = 30
	captchaText     = "Чтобы отправить сообщение в поддержку, подтвердите, что вы не бот: нажмите кнопку ниже. Сообщение будет передано сразу после этого."
	spamWarnMinConf = 0.8 // the LLM must be at least this sure to warn the operators
)

var (
	reLink    = regexp.MustCompile(`(?i)(https?://|www\.|\bt\.me/|\btelegram\.(me|dog)/|\btg://)`)
	reMention = regexp.MustCompile(`(?:^|[\s(])@[A-Za-z][A-Za-z0-9_]{3,31}\b`)
)

// foreignScripts are writing systems a Russian-speaking desk does not expect from real customers.
// Latin and Cyrillic are deliberately not listed: English and Ukrainian are ordinary.
var foreignScripts = []struct {
	name string
	tab  *unicode.RangeTable
}{
	{"китайский", unicode.Han},
	{"японский", unicode.Hiragana},
	{"японский", unicode.Katakana},
	{"корейский", unicode.Hangul},
	{"арабский", unicode.Arabic},
	{"тайский", unicode.Thai},
	{"деванагари", unicode.Devanagari},
	{"бенгальский", unicode.Bengali},
	{"иврит", unicode.Hebrew},
}

// foreignScript returns the name of a foreign writing system that makes up a noticeable part of
// the text ("" when the text is fine).
func foreignScript(text string) string {
	letters := 0
	counts := make([]int, len(foreignScripts))
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		for i, sc := range foreignScripts {
			if unicode.Is(sc.tab, r) {
				counts[i]++
				break
			}
		}
	}
	best, bestN := -1, 0
	for i, n := range counts {
		if n > bestN {
			best, bestN = i, n
		}
	}
	if best >= 0 && bestN >= 3 && bestN*100/letters >= 30 {
		return foreignScripts[best].name
	}
	return ""
}

// spamReasons lists why a message of an unverified user looks like spam.
func spamReasons(in UserMessage) []string {
	var out []string
	if reLink.MatchString(in.Text) {
		out = append(out, "ссылка")
	}
	if reMention.MatchString(in.Text) {
		out = append(out, "@упоминание")
	}
	if in.Forwarded {
		out = append(out, "пересланное сообщение")
	}
	if name := foreignScript(in.Text); name != "" {
		out = append(out, "текст на языке: "+name)
	}
	return out
}

// screen decides what happens to a message of an unverified user. It returns true when the
// message was held (the caller must not relay it). The caller holds the user's lock.
func (s *HelpdeskService) screen(ctx context.Context, u *domain.HelpdeskUser, in UserMessage, st domain.HelpdeskSettings) (bool, error) {
	switch {
	case u.Hold != "":
		held, err := s.hold(ctx, u, in)
		if err == nil && u.Hold == domain.HoldCaptcha && held%5 == 1 && held > 1 {
			s.resendCaptcha(ctx, u) // a reminder: the user keeps writing instead of pressing the button
		}
		return true, err
	case st.SpamCaptcha:
		if _, err := s.transport.SendCaptcha(ctx, u.UserID, captchaText); err != nil {
			if errors.Is(err, domain.ErrUserBlocked) {
				u.Blocked = true
				return true, s.repo.SaveUser(ctx, u)
			}
			s.log.Warn("send captcha, letting the message through", "user_id", u.UserID, "err", err)
			return false, nil
		}
		u.Hold = domain.HoldCaptcha
		_, err := s.hold(ctx, u, in)
		return true, err
	case st.SpamScreen:
		reasons := spamReasons(in)
		if len(reasons) == 0 {
			return false, nil
		}
		if err := s.postQuarantineCard(ctx, u, in, reasons); err != nil {
			s.log.Warn("post quarantine card, letting the message through", "user_id", u.UserID, "err", err)
			return false, nil
		}
		u.Hold = domain.HoldReview
		_, err := s.hold(ctx, u, in)
		return true, err
	}
	return false, nil
}

// hold keeps the message aside and saves the user; it returns how many messages are held now.
func (s *HelpdeskService) hold(ctx context.Context, u *domain.HelpdeskUser, in UserMessage) (int, error) {
	held, err := s.repo.HeldMessages(ctx, s.botDBID, u.UserID)
	if err != nil {
		return 0, err
	}
	n := len(held)
	if n < maxHeldPerUser {
		err = s.repo.AddHeld(ctx, &domain.HeldMessage{BotID: s.botDBID, UserID: u.UserID, MessageID: in.MessageID,
			MediaGroupID: in.MediaGroupID, ReplyToID: in.ReplyToID, Text: in.Text, SentAt: in.Date})
		if err != nil {
			return 0, err
		}
		n++
	}
	s.log.Info("user message held", "user_id", u.UserID, "hold", u.Hold, "held", n)
	return n, s.repo.SaveUser(ctx, u)
}

func (s *HelpdeskService) resendCaptcha(ctx context.Context, u *domain.HelpdeskUser) {
	if _, err := s.transport.SendCaptcha(ctx, u.UserID, captchaText); err != nil {
		s.log.Debug("resend captcha", "user_id", u.UserID, "err", err)
	}
}

func (s *HelpdeskService) postQuarantineCard(ctx context.Context, u *domain.HelpdeskUser, in UserMessage, reasons []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "🛡 <b>Сообщение новичка на проверке</b>\n👤 <b>%s</b>", html.EscapeString(u.Name))
	if u.Username != "" {
		fmt.Fprintf(&b, " (@%s)", html.EscapeString(u.Username))
	}
	fmt.Fprintf(&b, "\n🆔 <code>%d</code> · <a href=\"tg://user?id=%d\">профиль</a>", u.UserID, u.UserID)
	fmt.Fprintf(&b, "\n⚠️ Похоже на спам: %s", html.EscapeString(strings.Join(reasons, ", ")))
	if text := truncRunes(in.Text, 600); text != "" {
		fmt.Fprintf(&b, "\n\n<blockquote expandable>%s</blockquote>", html.EscapeString(text))
	}
	b.WriteString("\n\n<i>«Пропустить» передаст сообщение в тему пользователя и перестанет проверять его; «Спам» забанит.</i>")

	group, topic, err := s.QuarantineTopic(ctx)
	if err != nil {
		return err
	}
	if _, err = s.transport.SendQuarantineCard(ctx, group, topic, b.String(), u.UserID); errors.Is(err, domain.ErrTopicGone) {
		s.forgetQuarantineTopic(ctx, group)
		if group, topic, err = s.QuarantineTopic(ctx); err == nil {
			_, err = s.transport.SendQuarantineCard(ctx, group, topic, b.String(), u.UserID)
		}
	}
	return err
}

// Approve lets a held user through: the held messages are relayed and the user is verified, so
// their next messages are no longer screened.
func (s *HelpdeskService) Approve(ctx context.Context, userID int64) error {
	return s.release(ctx, userID, "")
}

// PassCaptcha is called when the user presses the captcha button. It returns ErrNotFound when the
// user has no pending captcha (already verified, or the prompt is stale).
func (s *HelpdeskService) PassCaptcha(ctx context.Context, userID int64) error {
	return s.release(ctx, userID, domain.HoldCaptcha)
}

func (s *HelpdeskService) release(ctx context.Context, userID int64, onlyHold string) error {
	st := s.cfg.Get()
	if !st.Active() {
		return domain.ErrHelpdeskOff
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		return err
	}
	if u.Banned {
		return errBanned()
	}
	if onlyHold != "" && u.Hold != onlyHold {
		return domain.ErrNotFound
	}
	held, err := s.repo.HeldMessages(ctx, s.botDBID, userID)
	if err != nil {
		return err
	}
	u.Verified, u.Hold = true, ""
	if err := s.repo.SaveUser(ctx, u); err != nil {
		return err
	}
	for _, m := range held {
		in := UserMessage{UserID: userID, Name: u.Name, Username: u.Username, MessageID: m.MessageID,
			MediaGroupID: m.MediaGroupID, ReplyToID: m.ReplyToID, Text: m.Text, Date: m.SentAt}
		if err := s.deliver(ctx, u, st, in, false); err != nil {
			s.log.Error("relay held message", "user_id", userID, "err", err)
		}
	}
	if err := s.repo.DeleteHeld(ctx, s.botDBID, userID); err != nil {
		s.log.Warn("delete held messages", "user_id", userID, "err", err)
	}
	s.log.Info("held user released", "user_id", userID, "messages", len(held))
	return nil
}

// HeldMessages returns the messages of a held user waiting for a decision.
func (s *HelpdeskService) HeldMessages(ctx context.Context, userID int64) ([]domain.HeldMessage, error) {
	return s.repo.HeldMessages(ctx, s.botDBID, userID)
}

// SuspectSpam is called when the LLM classified a message of the user as spam with confidence conf.
// It returns the user when the operators should be warned: only unverified, not yet banned users,
// and only once — a verified user is never flagged, and nothing is banned automatically.
func (s *HelpdeskService) SuspectSpam(ctx context.Context, userID int64, conf float64) (*domain.HelpdeskUser, bool) {
	if conf < spamWarnMinConf {
		return nil, false
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil || u.Verified || u.Banned || u.SpamFlagged {
		return nil, false
	}
	u.SpamFlagged = true
	if err := s.repo.SaveUser(ctx, u); err != nil {
		s.log.Warn("save spam flag", "user_id", userID, "err", err)
		return nil, false
	}
	return u, true
}
