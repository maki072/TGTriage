package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// TicketDismisser closes a user's open tickets as "not a task" when the user is banned as spam.
type TicketDismisser interface {
	DismissHelpdeskTickets(ctx context.Context, botID, userID int64) error
}

// IsSpamCommand reports the "/spam" command that bans the topic's user.
func IsSpamCommand(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "/spam" || strings.HasPrefix(raw, "/spam@") || strings.HasPrefix(raw, "/spam ")
}

// SetBanned bans the user as spam or lifts the ban. A banned user's messages are dropped silently
// (nothing is relayed, stored or answered), their topic is closed and their open tickets are
// dismissed. Unbanning only lets them write again: the next message reopens the topic.
func (s *HelpdeskService) SetBanned(ctx context.Context, userID int64, banned bool) (*domain.HelpdeskUser, error) {
	lock := s.userLock(userID)
	lock.Lock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if u.Banned == banned {
		lock.Unlock()
		return u, nil
	}
	u.Banned = banned
	u.BannedAt = nil
	if !banned {
		u.Verified = true // an operator lifted the ban: no more screening for this user
	}
	who := ActorFrom(ctx).Name
	if who == "" {
		who = "веб-панель"
	}
	text := "✅ <b>Пользователь разбанен</b> · " + html.EscapeString(who)
	if banned {
		now := time.Now()
		u.BannedAt = &now
		u.AwaitingSince, u.RemindedAt, u.Hold = nil, nil, ""
		text = "🚫 <b>Пользователь забанен как спам</b> · " + html.EscapeString(who) +
			"\nЕго сообщения игнорируются. Разбанить можно в веб-панели: Диалоги → Спам."
	}
	group := s.GroupID()
	if group != 0 && u.HasTopic(group) {
		if _, err := s.transport.SendHTML(ctx, group, u.TopicID, text, 0); err != nil {
			s.log.Debug("post ban notice", "user_id", userID, "err", err)
		}
		if banned && !u.TopicClosed {
			if err := s.transport.CloseTopic(ctx, group, u.TopicID); err != nil && !errors.Is(err, domain.ErrTopicGone) {
				s.log.Warn("close banned user's topic", "user_id", userID, "err", err)
			} else {
				u.TopicClosed = true
			}
		}
	}
	err = s.repo.SaveUser(ctx, u)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	s.log.Info("helpdesk user ban changed", "user_id", userID, "banned", banned, "by", who)
	if banned {
		if err := s.repo.DeleteHeld(ctx, s.botDBID, userID); err != nil {
			s.log.Warn("drop banned user's held messages", "user_id", userID, "err", err)
		}
	}
	if banned && s.tickets != nil {
		if err := s.tickets.DismissHelpdeskTickets(ctx, s.botDBID, userID); err != nil {
			s.log.Warn("dismiss banned user's tickets", "user_id", userID, "err", err)
		}
	}
	return u, nil
}

func errBanned() error {
	return fmt.Errorf("%w: пользователь забанен", domain.ErrInvalidInput)
}
