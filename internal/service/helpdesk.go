package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"tgtriage/internal/domain"
)

// HelpdeskTransport is the Telegram side of the support desk. Implementations translate "topic deleted"
// into domain.ErrTopicGone and "bot blocked by the user" into domain.ErrUserBlocked.
type HelpdeskTransport interface {
	CreateTopic(ctx context.Context, groupID int64, name string) (int, error)
	EditTopic(ctx context.Context, groupID int64, topicID int, name string) error
	CloseTopic(ctx context.Context, groupID int64, topicID int) error
	ReopenTopic(ctx context.Context, groupID int64, topicID int) error
	// Copy copies messages without the author (an album keeps its grouping) and returns the copies' ids.
	Copy(ctx context.Context, fromChatID int64, ids []int, toChatID int64, topicID, replyTo int) ([]int, error)
	SendHTML(ctx context.Context, chatID int64, topicID int, text string, replyTo int) (int, error)
	// SendReminder posts the "user is waiting" reminder with a "snooze" button.
	SendReminder(ctx context.Context, groupID int64, topicID int, text string, userID int64) (int, error)
	// SendUserHeader posts the user card that opens a topic, with a button that bans the user as spam.
	SendUserHeader(ctx context.Context, groupID int64, topicID int, text string, userID int64) (int, error)
	// SendCaptcha sends the "I am not a bot" prompt with its button to the user.
	SendCaptcha(ctx context.Context, userID int64, text string) (int, error)
	// SendQuarantineCard posts a held user's card with "approve" and "spam" buttons.
	SendQuarantineCard(ctx context.Context, groupID int64, topicID int, text string, userID int64) (int, error)
	EditText(ctx context.Context, chatID int64, messageID int, text string, entities json.RawMessage, caption bool) error
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
	IsChatMember(ctx context.Context, chatID, userID int64) (bool, error)
	CheckGroup(ctx context.Context, groupID int64) (*GroupCheck, error)
}

// GroupCheck reports whether the helpdesk group is set up correctly.
type GroupCheck struct {
	Title             string `json:"title"`
	IsForum           bool   `json:"is_forum"`
	BotAdmin          bool   `json:"bot_admin"`
	CanManageTopics   bool   `json:"can_manage_topics"`
	CanDeleteMessages bool   `json:"can_delete_messages"`
	CanPinMessages    bool   `json:"can_pin_messages"`
}

// UserMessage is a message a user sent to the bot in private.
type UserMessage struct {
	UserID       int64
	Name         string
	Username     string
	LanguageCode string
	MessageID    int
	MediaGroupID string
	ReplyToID    int
	Text         string // textual content for history and triage (media described in brackets)
	Start        bool   // /start command
	StartParam   string
	Forwarded    bool // the message is a forward of someone else's
	Date         time.Time
}

// OperatorMessage is a message written in a user's topic of the helpdesk group.
type OperatorMessage struct {
	GroupID      int64
	TopicID      int
	MessageID    int
	MediaGroupID string
	ReplyToID    int    // 0 when not a reply (topic root excluded)
	ReplyText    string // content of the replied message
	OperatorID   int64
	OperatorName string
	RawText      string // text or caption as typed: notes and commands are detected on it
	Text         string // textual content for history
	Date         time.Time
}

// ForwardedMessage is a message an operator forwarded to the bot in private.
type ForwardedMessage struct {
	OriginUserID int64 // author of the original message, 0 if hidden
	OriginDate   time.Time
	Text         string
}

const (
	albumDebounce       = 1200 * time.Millisecond
	helpdeskFwdDebounce = 2 * time.Second
	ticketsTopicName    = "🎫 Тикеты"
	supportSenderName   = "Поддержка"
)

// HelpdeskService relays messages between users (private chat with the bot) and operators (forum
// topics of the helpdesk group), keeps the user list and turns conversations into tickets.
type HelpdeskService struct {
	repo      domain.HelpdeskRepository
	messages  domain.MessageRepository
	settings  *SettingsService
	cfg       HelpdeskConfigStore // this bot's own helpdesk config (global Settings for the main bot)
	transport HelpdeskTransport
	triage    *TriageService
	tickets   TicketDismisser
	ownerID   int64
	botID     int64 // the Telegram bot's own user id (for "message from myself" checks)
	botDBID   int64 // 0 for the main bot, otherwise the row id in the bots table
	log       *slog.Logger

	mu           sync.Mutex
	locks        map[int64]*sync.Mutex
	albums       map[string]*pendingAlbum
	members      map[int64]memberEntry
	forwards     map[int64]*pendingForward // by operator
	ticketsMu    sync.Mutex
	quarantineMu sync.Mutex

	wg      sync.WaitGroup
	baseCtx context.Context
}

type pendingAlbum struct {
	user  []UserMessage
	op    []OperatorMessage
	timer *time.Timer
}

type memberEntry struct {
	groupID int64
	ok      bool
	until   time.Time
}

type forwardItem struct {
	user *domain.HelpdeskUser
	msg  domain.Message
}

type pendingForward struct {
	items []forwardItem
	timer *time.Timer
}

func NewHelpdeskService(repo domain.HelpdeskRepository, messages domain.MessageRepository, settings *SettingsService,
	cfg HelpdeskConfigStore, transport HelpdeskTransport, ownerID, botID, botDBID int64, log *slog.Logger) *HelpdeskService {
	return &HelpdeskService{
		repo: repo, messages: messages, settings: settings, cfg: cfg, transport: transport,
		ownerID: ownerID, botID: botID, botDBID: botDBID,
		log:   log.With("component", "helpdesk", "bot_db_id", botDBID),
		locks: map[int64]*sync.Mutex{}, albums: map[string]*pendingAlbum{}, members: map[int64]memberEntry{},
		forwards: map[int64]*pendingForward{}, baseCtx: context.Background(),
	}
}

// SetTriage wires the triage service (it is created after the helpdesk).
func (s *HelpdeskService) SetTriage(t *TriageService) { s.triage = t }

// SetTickets wires the ticket closer used when a user is banned.
func (s *HelpdeskService) SetTickets(t TicketDismisser) { s.tickets = t }

// Start remembers the service lifetime context for background work.
func (s *HelpdeskService) Start(ctx context.Context) { s.baseCtx = ctx }

// Wait blocks until background relays finish or timeout expires.
func (s *HelpdeskService) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Active reports whether the desk is enabled and configured.
func (s *HelpdeskService) Active() bool { return s.cfg.Get().Active() }

// GroupID returns the configured helpdesk group (0 when the desk is off).
func (s *HelpdeskService) GroupID() int64 {
	if h := s.cfg.Get(); h.Active() {
		return h.GroupID
	}
	return 0
}

// BotDBID is 0 for the main bot's desk, otherwise the row id of the additional bot it belongs to.
func (s *HelpdeskService) BotDBID() int64 { return s.botDBID }

// ConfiguredGroupID returns the group set for this bot's desk regardless of the enabled switch —
// for "is this chat the one bound to my desk" checks that must still fire while it's disabled.
func (s *HelpdeskService) ConfiguredGroupID() int64 { return s.cfg.Get().GroupID }

// Config returns this bot's own helpdesk configuration, for display (e.g. the /menu screen).
func (s *HelpdeskService) Config() domain.HelpdeskSettings { return s.cfg.Get() }

// UpdateConfig applies fn to this bot's own helpdesk config (the shared Settings for the main
// bot, or its own row otherwise) — used by the chat "use this group for the helpdesk" offer.
func (s *HelpdeskService) UpdateConfig(ctx context.Context, fn func(*domain.HelpdeskSettings)) error {
	return s.cfg.Update(ctx, fn)
}

func (s *HelpdeskService) userLock(userID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[userID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[userID] = l
	}
	return l
}

func (s *HelpdeskService) detached(name string, fn func(ctx context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("panic in helpdesk background job", "job", name, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), 5*time.Minute)
		defer cancel()
		fn(ctx)
	}()
}

// ---------- users → topics ----------

// OnUserMessage handles a private message from a user: relays it into their topic (creating one on
// the first message), stores it for history and triage and sends auto-replies.
func (s *HelpdeskService) OnUserMessage(ctx context.Context, in UserMessage) error {
	st := s.cfg.Get()
	if !st.Active() {
		return domain.ErrHelpdeskOff
	}
	lock := s.userLock(in.UserID)
	lock.Lock()
	defer lock.Unlock()

	u, err := s.repo.GetUser(ctx, s.botDBID, in.UserID)
	isNew := errors.Is(err, domain.ErrNotFound)
	if err != nil && !isNew {
		return err
	}
	if isNew {
		u = &domain.HelpdeskUser{BotID: s.botDBID, UserID: in.UserID}
	} else if u.Banned {
		s.log.Debug("message from a banned user dropped", "user_id", in.UserID)
		return nil
	}
	renamed := !isNew && (u.Name != in.Name || u.Username != in.Username)
	u.Name, u.Username = in.Name, in.Username
	if in.LanguageCode != "" {
		u.LanguageCode = in.LanguageCode
	}
	u.Blocked = false

	if in.Start {
		if in.StartParam != "" && u.Source == "" {
			u.Source = in.StartParam
		}
		if err := s.repo.SaveUser(ctx, u); err != nil {
			return err
		}
		if st.GreetingEnabled && strings.TrimSpace(st.GreetingText) != "" {
			s.sendToUser(ctx, u, st.GroupID, st.GreetingText, false)
		}
		return s.repo.SaveUser(ctx, u)
	}

	if !u.Verified {
		if held, err := s.screen(ctx, u, in, st); held || err != nil {
			return err
		}
	}
	return s.deliver(ctx, u, st, in, renamed)
}

// deliver relays a user's message into their topic (creating or reopening it), stores it for history
// and triage and sends the auto-reply. The caller holds the user's lock.
func (s *HelpdeskService) deliver(ctx context.Context, u *domain.HelpdeskUser, st domain.HelpdeskSettings, in UserMessage, renamed bool) error {
	now := time.Now()
	u.LastMessageAt = &now
	firstInCycle := u.AwaitingSince == nil
	if firstInCycle {
		u.AwaitingSince = &now
		u.RemindedAt = nil
	}
	if err := s.ensureTopic(ctx, u, st.GroupID); err != nil {
		return err
	}
	if renamed {
		if err := s.transport.EditTopic(ctx, st.GroupID, u.TopicID, topicName(u)); err != nil {
			s.log.Debug("rename topic", "user_id", u.UserID, "err", err)
		}
	}
	if in.MediaGroupID != "" {
		s.bufferAlbum("u:"+strconv.FormatInt(in.UserID, 10)+":"+in.MediaGroupID,
			func(a *pendingAlbum) { a.user = append(a.user, in) },
			func(ctx context.Context, a *pendingAlbum) { s.flushUserAlbum(ctx, in.UserID, a.user) })
	} else if err := s.relayFromUser(ctx, u, st.GroupID, []UserMessage{in}); err != nil {
		s.log.Error("relay user message", "user_id", u.UserID, "err", err)
	}
	s.storeIncoming(ctx, u, in, st)
	if firstInCycle {
		s.autoReply(ctx, u, st, now)
	}
	return s.repo.SaveUser(ctx, u)
}

func (s *HelpdeskService) flushUserAlbum(ctx context.Context, userID int64, msgs []UserMessage) {
	st := s.cfg.Get()
	if !st.Active() {
		return
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		s.log.Error("album: load user", "user_id", userID, "err", err)
		return
	}
	if u.Banned {
		return // banned while the album was being collected
	}
	if err := s.ensureTopic(ctx, u, st.GroupID); err != nil {
		s.log.Error("album: ensure topic", "user_id", userID, "err", err)
		return
	}
	slices.SortFunc(msgs, func(a, b UserMessage) int { return a.MessageID - b.MessageID })
	if err := s.relayFromUser(ctx, u, st.GroupID, msgs); err != nil {
		s.log.Error("relay user album", "user_id", userID, "err", err)
	}
}

// ensureTopic creates the user's topic when missing and reopens a closed one. Saves the user.
func (s *HelpdeskService) ensureTopic(ctx context.Context, u *domain.HelpdeskUser, groupID int64) error {
	if !u.HasTopic(groupID) {
		return s.createTopic(ctx, u, groupID)
	}
	if u.TopicClosed {
		if err := s.transport.ReopenTopic(ctx, groupID, u.TopicID); err != nil {
			if errors.Is(err, domain.ErrTopicGone) {
				return s.createTopic(ctx, u, groupID)
			}
			s.log.Warn("reopen topic", "user_id", u.UserID, "err", err)
		}
		u.TopicClosed = false
	}
	return s.repo.SaveUser(ctx, u)
}

func (s *HelpdeskService) createTopic(ctx context.Context, u *domain.HelpdeskUser, groupID int64) error {
	id, err := s.transport.CreateTopic(ctx, groupID, topicName(u))
	if err != nil {
		return fmt.Errorf("create topic: %w", err)
	}
	u.GroupID, u.TopicID, u.TopicClosed = groupID, id, false
	if err := s.repo.SaveUser(ctx, u); err != nil {
		return err
	}
	if _, err := s.transport.SendUserHeader(ctx, groupID, id, userHeader(u), u.UserID); err != nil {
		s.log.Warn("post topic header", "user_id", u.UserID, "err", err)
	}
	s.log.Info("helpdesk topic created", "user_id", u.UserID, "topic_id", id)
	return nil
}

func (s *HelpdeskService) relayFromUser(ctx context.Context, u *domain.HelpdeskUser, groupID int64, msgs []UserMessage) error {
	ids := make([]int, len(msgs))
	for i, m := range msgs {
		ids[i] = m.MessageID
	}
	replyTo := 0
	if r := msgs[0].ReplyToID; r != 0 {
		if m, err := s.repo.MessageByUserMsg(ctx, s.botDBID, u.UserID, r); err == nil && m.GroupID == groupID {
			replyTo = m.GroupMsgID
		}
	}
	copies, err := s.transport.Copy(ctx, u.UserID, ids, groupID, u.TopicID, replyTo)
	if errors.Is(err, domain.ErrTopicGone) || errors.Is(err, domain.ErrTopicClosed) {
		if errors.Is(err, domain.ErrTopicGone) {
			err = s.createTopic(ctx, u, groupID)
		} else {
			u.TopicClosed = true
			err = s.ensureTopic(ctx, u, groupID)
		}
		if err != nil {
			return err
		}
		copies, err = s.transport.Copy(ctx, u.UserID, ids, groupID, u.TopicID, 0)
	}
	if err != nil {
		return err
	}
	now := time.Now()
	for i, id := range ids {
		if i >= len(copies) {
			break
		}
		if err := s.repo.SaveMessage(ctx, &domain.HelpdeskMessage{BotID: s.botDBID, UserID: u.UserID, Direction: domain.HelpdeskIn,
			UserMsgID: id, GroupID: groupID, GroupMsgID: copies[i], CreatedAt: now}); err != nil {
			s.log.Warn("save message mapping", "err", err)
		}
	}
	return nil
}

func (s *HelpdeskService) storeIncoming(ctx context.Context, u *domain.HelpdeskUser, in UserMessage, st domain.HelpdeskSettings) {
	if strings.TrimSpace(in.Text) == "" {
		return
	}
	dm := &domain.Message{ConnectionID: domain.HelpdeskConnectionFor(s.botDBID), ChatID: u.UserID, MessageID: in.MessageID,
		SenderID: u.UserID, SenderName: u.Name, SenderUsername: u.Username, Text: in.Text, SentAt: in.Date}
	var err error
	if st.TriageEnabled && s.triage != nil {
		err = s.triage.OnIncoming(ctx, dm)
	} else {
		dm.Analyzed = true
		_, err = s.messages.Save(ctx, dm)
	}
	if err != nil {
		s.log.Error("store helpdesk message", "user_id", u.UserID, "err", err)
	}
}

func (s *HelpdeskService) autoReply(ctx context.Context, u *domain.HelpdeskUser, st domain.HelpdeskSettings, now time.Time) {
	var text string
	switch {
	case st.HoursEnabled && !st.InWorkingHours(now.In(s.settings.Location())) && strings.TrimSpace(st.OffHoursText) != "":
		text = strings.ReplaceAll(st.OffHoursText, "{hours}", st.HoursLabel())
	case st.AutoReplyEnabled:
		text = st.AutoReplyText
	}
	if strings.TrimSpace(text) != "" {
		s.sendToUser(ctx, u, st.GroupID, text, true)
	}
}

// sendToUser sends a bot text (greeting, auto-reply) to the user, optionally mirrored into the topic.
func (s *HelpdeskService) sendToUser(ctx context.Context, u *domain.HelpdeskUser, groupID int64, text string, mirror bool) {
	msgID, err := s.transport.SendHTML(ctx, u.UserID, 0, html.EscapeString(text), 0)
	if err != nil {
		if errors.Is(err, domain.ErrUserBlocked) {
			u.Blocked = true
		}
		s.log.Warn("send auto message", "user_id", u.UserID, "err", err)
		return
	}
	s.storeOutgoing(ctx, u.UserID, msgID, 0, text)
	if !mirror || !u.HasTopic(groupID) {
		return
	}
	mirrorID, err := s.transport.SendHTML(ctx, groupID, u.TopicID, "🤖 <i>Автоответ пользователю:</i>\n"+html.EscapeString(text), 0)
	if err != nil {
		s.log.Debug("mirror auto message", "err", err)
		return
	}
	_ = s.repo.SaveMessage(ctx, &domain.HelpdeskMessage{BotID: s.botDBID, UserID: u.UserID, Direction: domain.HelpdeskOut,
		UserMsgID: msgID, GroupID: groupID, GroupMsgID: mirrorID})
}

func (s *HelpdeskService) storeOutgoing(ctx context.Context, userID int64, msgID int, operatorID int64, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if _, err := s.messages.Save(ctx, &domain.Message{ConnectionID: domain.HelpdeskConnectionFor(s.botDBID), ChatID: userID,
		MessageID: msgID, SenderID: operatorID, SenderName: supportSenderName, Outgoing: true, Text: text,
		SentAt: time.Now(), Analyzed: true}); err != nil {
		s.log.Warn("store outgoing helpdesk message", "user_id", userID, "err", err)
	}
}

// ---------- topics → users ----------

// IsForceTicketCommand reports the "/1" command that turns the replied message into a ticket.
func IsForceTicketCommand(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "/1" || strings.HasPrefix(raw, "/1@") || strings.HasPrefix(raw, "/1 ")
}

// IsInternalNote reports a topic message that must not reach the user.
func IsInternalNote(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "!")
}

// OnOperatorMessage relays an operator's message from a topic to the user anonymously. Internal notes
// ("//", "!") stay in the topic; "/1" creates a ticket from the replied message.
func (s *HelpdeskService) OnOperatorMessage(ctx context.Context, in OperatorMessage) error {
	st := s.cfg.Get()
	if !st.Active() || in.GroupID != st.GroupID || in.TopicID == 0 {
		return nil
	}
	u, err := s.repo.UserByTopic(ctx, s.botDBID, in.GroupID, in.TopicID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil // General, the tickets topic or a topic created by hand
	}
	if err != nil {
		return err
	}
	switch {
	case IsSpamCommand(in.RawText):
		if _, err := s.SetBanned(ctx, u.UserID, true); err != nil {
			return err
		}
		if err := s.transport.DeleteMessage(ctx, in.GroupID, in.MessageID); err != nil {
			s.log.Debug("delete /spam command", "err", err) // needs the "delete messages" admin right
		}
		return nil
	case IsForceTicketCommand(in.RawText):
		userID := u.UserID
		s.detached("force ticket", func(ctx context.Context) { s.forceTicket(ctx, userID, in) })
		return nil
	case IsInternalNote(in.RawText):
		return nil
	case in.MediaGroupID != "":
		userID := u.UserID
		s.bufferAlbum("g:"+in.MediaGroupID,
			func(a *pendingAlbum) { a.op = append(a.op, in) },
			func(ctx context.Context, a *pendingAlbum) { s.flushOperatorAlbum(ctx, userID, a.op) })
		return nil
	}
	lock := s.userLock(u.UserID)
	lock.Lock()
	defer lock.Unlock()
	if u, err = s.repo.GetUser(ctx, s.botDBID, u.UserID); err != nil {
		return err
	}
	return s.relayFromOperator(ctx, u, []OperatorMessage{in})
}

func (s *HelpdeskService) flushOperatorAlbum(ctx context.Context, userID int64, msgs []OperatorMessage) {
	for _, m := range msgs {
		if IsInternalNote(m.RawText) {
			return
		}
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		s.log.Error("operator album: load user", "user_id", userID, "err", err)
		return
	}
	slices.SortFunc(msgs, func(a, b OperatorMessage) int { return a.MessageID - b.MessageID })
	if err := s.relayFromOperator(ctx, u, msgs); err != nil {
		s.log.Error("relay operator album", "user_id", userID, "err", err)
	}
}

func (s *HelpdeskService) relayFromOperator(ctx context.Context, u *domain.HelpdeskUser, msgs []OperatorMessage) error {
	first := msgs[0]
	ids := make([]int, len(msgs))
	for i, m := range msgs {
		ids[i] = m.MessageID
	}
	replyTo := 0
	if first.ReplyToID != 0 {
		if m, err := s.repo.MessageByGroupMsg(ctx, s.botDBID, first.GroupID, first.ReplyToID); err == nil && m.UserID == u.UserID {
			replyTo = m.UserMsgID
		}
	}
	copies, err := s.transport.Copy(ctx, first.GroupID, ids, u.UserID, 0, replyTo)
	if err != nil {
		notice := "⚠️ <b>Не доставлено.</b> "
		if errors.Is(err, domain.ErrUserBlocked) {
			u.Blocked = true
			notice += "Пользователь заблокировал бота."
		} else {
			notice += html.EscapeString(truncRunes(err.Error(), 300))
		}
		if _, nerr := s.transport.SendHTML(ctx, first.GroupID, first.TopicID, notice, first.MessageID); nerr != nil {
			s.log.Warn("post delivery failure notice", "err", nerr)
		}
		s.log.Warn("relay operator message", "user_id", u.UserID, "err", err)
		return s.repo.SaveUser(ctx, u)
	}
	now := time.Now()
	for i, m := range msgs {
		if i >= len(copies) {
			break
		}
		if err := s.repo.SaveMessage(ctx, &domain.HelpdeskMessage{BotID: s.botDBID, UserID: u.UserID, Direction: domain.HelpdeskOut,
			UserMsgID: copies[i], GroupID: first.GroupID, GroupMsgID: m.MessageID, OperatorID: m.OperatorID, CreatedAt: now}); err != nil {
			s.log.Warn("save message mapping", "err", err)
		}
		s.storeOutgoing(ctx, u.UserID, copies[i], m.OperatorID, m.Text)
	}
	u.AwaitingSince, u.RemindedAt, u.Blocked, u.Verified = nil, nil, false, true
	return s.repo.SaveUser(ctx, u)
}

// ---------- edits, topic state, blocking ----------

// OnUserEdited mirrors an edit of the user's message into the topic.
func (s *HelpdeskService) OnUserEdited(ctx context.Context, userID int64, messageID int, text string,
	entities json.RawMessage, caption bool, content string) error {
	if !s.Active() {
		return nil
	}
	m, err := s.repo.MessageByUserMsg(ctx, s.botDBID, userID, messageID)
	if err != nil || m.Direction != domain.HelpdeskIn {
		return nil
	}
	if strings.TrimSpace(content) != "" {
		if err := s.messages.UpdateText(ctx, domain.HelpdeskConnectionFor(s.botDBID), userID, messageID, content); err != nil {
			s.log.Warn("update edited message", "err", err)
		}
	}
	return s.transport.EditText(ctx, m.GroupID, m.GroupMsgID, text, entities, caption)
}

// OnOperatorEdited mirrors an edit of an operator's message into the user's chat.
func (s *HelpdeskService) OnOperatorEdited(ctx context.Context, groupID int64, messageID int, text string,
	entities json.RawMessage, caption bool, content string) error {
	if s.GroupID() != groupID {
		return nil
	}
	m, err := s.repo.MessageByGroupMsg(ctx, s.botDBID, groupID, messageID)
	if err != nil || m.Direction != domain.HelpdeskOut {
		return nil
	}
	if strings.TrimSpace(content) != "" {
		if err := s.messages.UpdateText(ctx, domain.HelpdeskConnectionFor(s.botDBID), m.UserID, m.UserMsgID, content); err != nil {
			s.log.Warn("update edited message", "err", err)
		}
	}
	return s.transport.EditText(ctx, m.UserID, m.UserMsgID, text, entities, caption)
}

// OnTopicState syncs a topic closed or reopened by hand in Telegram.
func (s *HelpdeskService) OnTopicState(ctx context.Context, groupID int64, topicID int, closed bool) error {
	u, err := s.repo.UserByTopic(ctx, s.botDBID, groupID, topicID)
	if err != nil {
		return nil
	}
	lock := s.userLock(u.UserID)
	lock.Lock()
	defer lock.Unlock()
	if u, err = s.repo.GetUser(ctx, s.botDBID, u.UserID); err != nil {
		return err
	}
	u.TopicClosed = closed
	if closed {
		u.AwaitingSince, u.RemindedAt = nil, nil
	}
	return s.repo.SaveUser(ctx, u)
}

// OnUserBlocked records that the user blocked (or unblocked) the bot and tells the operators.
func (s *HelpdeskService) OnUserBlocked(ctx context.Context, userID int64, blocked bool) error {
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		return nil
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	if u.Blocked == blocked {
		return nil
	}
	u.Blocked = blocked
	if blocked {
		u.AwaitingSince, u.RemindedAt = nil, nil
	}
	if err := s.repo.SaveUser(ctx, u); err != nil {
		return err
	}
	if group := s.GroupID(); group != 0 && u.HasTopic(group) {
		text := "🚫 <b>Пользователь заблокировал бота</b> — сообщения ему не доставляются."
		if !blocked {
			text = "✅ Пользователь снова разблокировал бота."
		}
		if _, err := s.transport.SendHTML(ctx, group, u.TopicID, text, 0); err != nil {
			s.log.Debug("post block notice", "err", err)
		}
	}
	return nil
}

// ---------- tickets ----------

func (s *HelpdeskService) forceTicket(ctx context.Context, userID int64, in OperatorMessage) {
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		s.log.Error("force ticket: load user", "err", err)
		return
	}
	msgs, err := s.ticketMessages(ctx, u, in.GroupID, in.ReplyToID, in.ReplyText)
	if err != nil || len(msgs) == 0 {
		s.notice(ctx, in.GroupID, in.TopicID, "ℹ️ Нечего оформлять: ответьте командой /1 на сообщение пользователя.", 0)
		return
	}
	if err := s.transport.DeleteMessage(ctx, in.GroupID, in.MessageID); err != nil {
		s.log.Debug("delete /1 command", "err", err) // needs the "delete messages" admin right
	}
	if s.triage == nil {
		return
	}
	if _, err := s.triage.CreateHelpdeskTicket(ctx, u, msgs); err != nil {
		s.notice(ctx, in.GroupID, in.TopicID, "❌ Не удалось создать тикет: "+html.EscapeString(truncRunes(err.Error(), 300)), 0)
	}
}

// ForceTicket creates a ticket from the user's latest unanswered messages (Mini App button).
func (s *HelpdeskService) ForceTicket(ctx context.Context, userID int64) (*domain.Task, error) {
	if !s.Active() {
		return nil, domain.ErrHelpdeskOff
	}
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.ticketMessages(ctx, u, 0, 0, "")
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("%w: у пользователя нет сообщений", domain.ErrInvalidInput)
	}
	if s.triage == nil {
		return nil, domain.ErrProviderUnset
	}
	return s.triage.CreateHelpdeskTicket(ctx, u, msgs)
}

// ticketMessages picks the messages a forced ticket is made of: the replied one, or the latest run of
// user messages after the last answer.
func (s *HelpdeskService) ticketMessages(ctx context.Context, u *domain.HelpdeskUser, groupID int64, replyTo int, replyText string) ([]domain.Message, error) {
	if replyTo != 0 {
		if hm, err := s.repo.MessageByGroupMsg(ctx, s.botDBID, groupID, replyTo); err == nil && hm.UserID == u.UserID {
			if dm, err := s.messages.Find(ctx, domain.HelpdeskConnectionFor(s.botDBID), u.UserID, hm.UserMsgID); err == nil {
				return []domain.Message{*dm}, nil
			}
			if strings.TrimSpace(replyText) != "" {
				return []domain.Message{{ConnectionID: domain.HelpdeskConnectionFor(s.botDBID), ChatID: u.UserID, MessageID: hm.UserMsgID,
					SenderID: u.UserID, SenderName: u.Name, SenderUsername: u.Username, Text: replyText,
					Outgoing: hm.Direction == domain.HelpdeskOut, SentAt: hm.CreatedAt}}, nil
			}
		}
		if strings.TrimSpace(replyText) != "" {
			return []domain.Message{{ConnectionID: domain.HelpdeskConnectionFor(s.botDBID), ChatID: u.UserID, SenderID: u.UserID,
				SenderName: u.Name, SenderUsername: u.Username, Text: replyText, SentAt: time.Now()}}, nil
		}
		return nil, nil
	}
	history, err := s.messages.History(ctx, domain.HelpdeskConnectionFor(s.botDBID), u.UserID, math.MaxInt64, 30)
	if err != nil {
		return nil, err
	}
	var run []domain.Message
	for i := len(history) - 1; i >= 0 && len(run) < 10; i-- {
		if history[i].Outgoing {
			if len(run) > 0 {
				break
			}
			continue
		}
		run = append(run, history[i])
	}
	slices.Reverse(run)
	return run, nil
}

// ResolveForward finds the helpdesk user a message forwarded to the bot came from: directly by the
// author, or — for a copy forwarded from a topic, whose author is the bot — by its time and text.
func (s *HelpdeskService) ResolveForward(ctx context.Context, f ForwardedMessage) (*domain.HelpdeskUser, *domain.Message) {
	st := s.cfg.Get()
	if !st.Active() {
		return nil, nil
	}
	if f.OriginUserID != 0 && f.OriginUserID != s.botID {
		u, err := s.repo.GetUser(ctx, s.botDBID, f.OriginUserID)
		if err != nil {
			return nil, nil
		}
		return u, &domain.Message{ConnectionID: domain.HelpdeskConnectionFor(s.botDBID), ChatID: u.UserID, SenderID: u.UserID,
			SenderName: u.Name, SenderUsername: u.Username, Text: f.Text, SentAt: f.OriginDate}
	}
	if f.OriginUserID != s.botID || f.OriginDate.IsZero() {
		return nil, nil
	}
	cands, err := s.repo.MessagesAround(ctx, s.botDBID, st.GroupID, f.OriginDate, 3*time.Second)
	if err != nil || len(cands) == 0 {
		return nil, nil
	}
	pick := func(hm domain.HelpdeskMessage) (*domain.HelpdeskUser, *domain.Message) {
		u, err := s.repo.GetUser(ctx, s.botDBID, hm.UserID)
		if err != nil {
			return nil, nil
		}
		dm, err := s.messages.Find(ctx, domain.HelpdeskConnectionFor(s.botDBID), hm.UserID, hm.UserMsgID)
		if err != nil {
			dm = &domain.Message{ConnectionID: domain.HelpdeskConnectionFor(s.botDBID), ChatID: u.UserID, MessageID: hm.UserMsgID,
				SenderID: u.UserID, SenderName: u.Name, SenderUsername: u.Username, Text: f.Text, SentAt: hm.CreatedAt}
		}
		return u, dm
	}
	for _, hm := range cands {
		if dm, err := s.messages.Find(ctx, domain.HelpdeskConnectionFor(s.botDBID), hm.UserID, hm.UserMsgID); err == nil &&
			strings.TrimSpace(dm.Text) == strings.TrimSpace(f.Text) {
			return pick(hm)
		}
	}
	if len(cands) == 1 {
		return pick(cands[0])
	}
	return nil, nil
}

// QueueForwardTicket collects messages an operator forwards in one go and turns each user's batch into a ticket.
// done is called with the created ticket (or error) once per user.
func (s *HelpdeskService) QueueForwardTicket(operatorID int64, u *domain.HelpdeskUser, m domain.Message,
	done func(ctx context.Context, t *domain.Task, err error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pf, ok := s.forwards[operatorID]
	if !ok {
		pf = &pendingForward{}
		s.forwards[operatorID] = pf
	}
	pf.items = append(pf.items, forwardItem{user: u, msg: m})
	if pf.timer != nil {
		pf.timer.Stop()
	}
	pf.timer = time.AfterFunc(helpdeskFwdDebounce, func() {
		s.mu.Lock()
		if s.forwards[operatorID] != pf {
			s.mu.Unlock()
			return
		}
		delete(s.forwards, operatorID)
		s.mu.Unlock()
		s.detached("forward ticket", func(ctx context.Context) {
			var order []int64
			byUser := map[int64][]domain.Message{}
			users := map[int64]*domain.HelpdeskUser{}
			for _, it := range pf.items {
				if _, ok := byUser[it.user.UserID]; !ok {
					order = append(order, it.user.UserID)
				}
				byUser[it.user.UserID] = append(byUser[it.user.UserID], it.msg)
				users[it.user.UserID] = it.user
			}
			for _, id := range order {
				if s.triage == nil {
					done(ctx, nil, domain.ErrProviderUnset)
					continue
				}
				t, err := s.triage.CreateHelpdeskTicket(ctx, users[id], byUser[id])
				done(ctx, t, err)
			}
		})
	})
}

// ---------- Mini App ----------

// ReplyToUser sends text to the user from the Mini App and mirrors it into the topic.
func (s *HelpdeskService) ReplyToUser(ctx context.Context, userID int64, text string) error {
	st := s.cfg.Get()
	if !st.Active() {
		return domain.ErrHelpdeskOff
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return domain.ErrEmptyReply
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
	msgID, err := s.transport.SendHTML(ctx, userID, 0, html.EscapeString(text), 0)
	if err != nil {
		if errors.Is(err, domain.ErrUserBlocked) {
			u.Blocked = true
			_ = s.repo.SaveUser(ctx, u)
		}
		return err
	}
	actor := ActorFrom(ctx)
	if u.HasTopic(st.GroupID) {
		who := actor.Name
		if who == "" {
			who = "веб-панель"
		}
		mirrorID, err := s.transport.SendHTML(ctx, st.GroupID, u.TopicID,
			"📤 <b>Отправлено пользователю</b> · "+html.EscapeString(who)+"\n\n"+html.EscapeString(text), 0)
		if err != nil {
			s.log.Debug("mirror panel reply", "err", err)
		} else {
			_ = s.repo.SaveMessage(ctx, &domain.HelpdeskMessage{BotID: s.botDBID, UserID: userID, Direction: domain.HelpdeskOut,
				UserMsgID: msgID, GroupID: st.GroupID, GroupMsgID: mirrorID, OperatorID: actor.ID})
		}
	}
	s.storeOutgoing(ctx, userID, msgID, actor.ID, text)
	u.AwaitingSince, u.RemindedAt, u.Blocked, u.Verified = nil, nil, false, true
	return s.repo.SaveUser(ctx, u)
}

// SetTopicClosed closes or reopens the user's topic from the Mini App.
func (s *HelpdeskService) SetTopicClosed(ctx context.Context, userID int64, closed bool) (*domain.HelpdeskUser, error) {
	st := s.cfg.Get()
	if !st.Active() {
		return nil, domain.ErrHelpdeskOff
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		return nil, err
	}
	if !closed {
		u.TopicClosed = true // force a reopen call even if our state drifted
		return u, s.ensureTopic(ctx, u, st.GroupID)
	}
	if u.HasTopic(st.GroupID) {
		if err := s.transport.CloseTopic(ctx, st.GroupID, u.TopicID); err != nil && !errors.Is(err, domain.ErrTopicGone) {
			return nil, err
		}
	}
	u.TopicClosed = true
	u.AwaitingSince, u.RemindedAt = nil, nil
	return u, s.repo.SaveUser(ctx, u)
}

// Users lists this bot's own helpdesk users for the dialogs screen.
func (s *HelpdeskService) Users(ctx context.Context, f domain.HelpdeskUserFilter) ([]domain.HelpdeskUser, int, error) {
	return s.repo.ListUsers(ctx, &s.botDBID, f)
}

// UsersAcrossBots lists helpdesk users merged over every bot (the owner's cross-organization
// dialogs view in the Mini App) — safe to call on any live HelpdeskService instance, since the
// underlying repository is shared.
func (s *HelpdeskService) UsersAcrossBots(ctx context.Context, f domain.HelpdeskUserFilter) ([]domain.HelpdeskUser, int, error) {
	return s.repo.ListUsers(ctx, nil, f)
}

// User returns one helpdesk user.
func (s *HelpdeskService) User(ctx context.Context, userID int64) (*domain.HelpdeskUser, error) {
	return s.repo.GetUser(ctx, s.botDBID, userID)
}

// Conversation returns the latest messages with the user, oldest first.
func (s *HelpdeskService) Conversation(ctx context.Context, userID int64, limit int) ([]domain.Message, error) {
	return s.messages.History(ctx, domain.HelpdeskConnectionFor(s.botDBID), userID, math.MaxInt64, limit)
}

// TopicURL returns a link to the user's topic, empty when there is none in the current group.
func (s *HelpdeskService) TopicURL(u *domain.HelpdeskUser) string {
	if g := s.GroupID(); g != 0 && u.HasTopic(g) {
		return domain.TopicLink(g, u.TopicID)
	}
	return ""
}

// IsOperator reports whether userID may work with the desk: the owner or a member of the helpdesk group.
func (s *HelpdeskService) IsOperator(ctx context.Context, userID int64) bool {
	if userID == s.ownerID {
		return true
	}
	group := s.GroupID()
	if group == 0 || userID <= 0 {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	e, ok := s.members[userID]
	s.mu.Unlock()
	if ok && e.groupID == group && now.Before(e.until) {
		return e.ok
	}
	member, err := s.transport.IsChatMember(ctx, group, userID)
	ttl := 5 * time.Minute
	if err != nil {
		s.log.Debug("check operator membership", "user_id", userID, "err", err)
		member, ttl = false, 30*time.Second
	} else if !member {
		ttl = time.Minute
	}
	s.mu.Lock()
	s.members[userID] = memberEntry{groupID: group, ok: member, until: now.Add(ttl)}
	s.mu.Unlock()
	return member
}

// CheckGroup verifies the configured group (forum mode, bot rights).
func (s *HelpdeskService) CheckGroup(ctx context.Context) (*GroupCheck, error) {
	group := s.cfg.Get().GroupID
	if group == 0 {
		return nil, fmt.Errorf("%w: не указан ID супергруппы", domain.ErrInvalidInput)
	}
	return s.transport.CheckGroup(ctx, group)
}

// ---------- ticket cards ----------

func ticketsTopicMetaKey(groupID int64) string { return fmt.Sprintf("hd_tickets_topic_%d", groupID) }

// TicketsTopic returns the group topic with all ticket cards, creating it when missing.
func (s *HelpdeskService) TicketsTopic(ctx context.Context) (int64, int, error) {
	return s.namedTopic(ctx, &s.ticketsMu, ticketsTopicMetaKey(s.GroupID()), ticketsTopicName)
}

// ForgetTicketsTopic drops a tickets topic that turned out to be deleted, together with the menu
// message that lived in it.
func (s *HelpdeskService) ForgetTicketsTopic(ctx context.Context, groupID int64) {
	for _, key := range []string{ticketsTopicMetaKey(groupID), ticketsMenuMetaKey(groupID)} {
		if err := s.settings.SetMeta(ctx, key, ""); err != nil {
			s.log.Warn("forget tickets topic", "err", err)
		}
	}
}

func ticketsMenuMetaKey(groupID int64) string { return fmt.Sprintf("hd_tickets_menu_%d", groupID) }

func ticketsPrunedMetaKey(groupID int64) string { return fmt.Sprintf("hd_tickets_pruned_%d", groupID) }

// TicketsTopicID returns the id of the tickets topic without creating it (0 when there is none).
func (s *HelpdeskService) TicketsTopicID(ctx context.Context) int {
	v, _ := s.settings.Meta(ctx, ticketsTopicMetaKey(s.GroupID()))
	id, _ := strconv.Atoi(v)
	return id
}

// TicketsMenuID returns the message id of the ticket management menu in the tickets topic (0 when
// it has not been posted yet).
func (s *HelpdeskService) TicketsMenuID(ctx context.Context, groupID int64) int {
	v, _ := s.settings.Meta(ctx, ticketsMenuMetaKey(groupID))
	id, _ := strconv.Atoi(v)
	return id
}

func (s *HelpdeskService) SetTicketsMenuID(ctx context.Context, groupID int64, id int) error {
	return s.settings.SetMeta(ctx, ticketsMenuMetaKey(groupID), strconv.Itoa(id))
}

// TicketsPruned reports whether the ticket cards that older versions duplicated into the tickets
// topic have already been cleaned up; MarkTicketsPruned records that.
func (s *HelpdeskService) TicketsPruned(ctx context.Context, groupID int64) bool {
	v, _ := s.settings.Meta(ctx, ticketsPrunedMetaKey(groupID))
	return v == "1"
}

func (s *HelpdeskService) MarkTicketsPruned(ctx context.Context, groupID int64) error {
	return s.settings.SetMeta(ctx, ticketsPrunedMetaKey(groupID), "1")
}

func (s *HelpdeskService) SaveCard(ctx context.Context, c domain.HelpdeskCard) error {
	return s.repo.SaveCard(ctx, c)
}

func (s *HelpdeskService) Cards(ctx context.Context, taskID int64) ([]domain.HelpdeskCard, error) {
	return s.repo.Cards(ctx, taskID)
}

func (s *HelpdeskService) CardsInTopic(ctx context.Context, groupID int64, topicID int) ([]domain.HelpdeskCard, error) {
	return s.repo.CardsInTopic(ctx, groupID, topicID)
}

func (s *HelpdeskService) DeleteCard(ctx context.Context, c domain.HelpdeskCard) error {
	return s.repo.DeleteCard(ctx, c)
}

// ---------- reminders ----------

// CheckReminders nudges operators in topics of users who wait for an answer too long.
func (s *HelpdeskService) CheckReminders(ctx context.Context) {
	h := s.cfg.Get()
	if !h.Active() || h.ReminderMinutes <= 0 {
		return
	}
	now := time.Now()
	if !h.InWorkingHours(now.In(s.settings.Location())) {
		return
	}
	users, err := s.repo.DueReminders(ctx, s.botDBID, now.Add(-time.Duration(h.ReminderMinutes)*time.Minute))
	if err != nil {
		s.log.Error("load due reminders", "err", err)
		return
	}
	for _, due := range users {
		if due.GroupID != h.GroupID {
			continue
		}
		s.remind(ctx, due.UserID, h, now)
	}
}

func (s *HelpdeskService) remind(ctx context.Context, userID int64, h domain.HelpdeskSettings, now time.Time) {
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil || u.AwaitingSince == nil || !u.HasTopic(h.GroupID) {
		return
	}
	text := fmt.Sprintf("⏰ <b>Пользователь ждёт ответа %s</b>", humanDuration(now.Sub(*u.AwaitingSince)))
	if _, err := s.transport.SendReminder(ctx, h.GroupID, u.TopicID, text, userID); err != nil {
		s.log.Warn("send reminder", "user_id", userID, "err", err)
		if errors.Is(err, domain.ErrTopicGone) {
			u.TopicID = 0
		}
	}
	u.RemindedAt = &now
	if err := s.repo.SaveUser(ctx, u); err != nil {
		s.log.Error("save reminder mark", "err", err)
	}
}

// SnoozeReminder holds back the "user is waiting" reminders about userID until the given moment;
// after that they resume at the usual interval. An operator's answer ends the wait and the snooze
// with it. RemindedAt is set so that the reminder comes due exactly at until.
func (s *HelpdeskService) SnoozeReminder(ctx context.Context, userID int64, until time.Time) error {
	if !until.After(time.Now()) {
		return domain.ErrInvalidInput
	}
	lock := s.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	u, err := s.repo.GetUser(ctx, s.botDBID, userID)
	if err != nil {
		return err
	}
	if u.AwaitingSince == nil {
		return fmt.Errorf("%w: пользователь уже получил ответ", domain.ErrInvalidInput)
	}
	mark := until.Add(-time.Duration(max(s.cfg.Get().ReminderMinutes, 0)) * time.Minute)
	u.RemindedAt = &mark
	return s.repo.SaveUser(ctx, u)
}

// CleanupMessages drops message mappings older than before.
func (s *HelpdeskService) CleanupMessages(ctx context.Context, before time.Time) (int64, error) {
	if _, err := s.repo.DeleteHeldOlderThan(ctx, before); err != nil {
		s.log.Warn("clean up held messages", "err", err)
	}
	return s.repo.DeleteMessagesOlderThan(ctx, before)
}

// ---------- helpers ----------

func (s *HelpdeskService) notice(ctx context.Context, groupID int64, topicID int, text string, replyTo int) {
	if _, err := s.transport.SendHTML(ctx, groupID, topicID, text, replyTo); err != nil {
		s.log.Debug("post notice", "err", err)
	}
}

func (s *HelpdeskService) bufferAlbum(key string, add func(*pendingAlbum), flush func(context.Context, *pendingAlbum)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.albums[key]
	if !ok {
		a = &pendingAlbum{}
		s.albums[key] = a
	}
	add(a)
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(albumDebounce, func() {
		s.mu.Lock()
		if s.albums[key] != a {
			s.mu.Unlock()
			return
		}
		delete(s.albums, key)
		s.mu.Unlock()
		s.detached("album", func(ctx context.Context) { flush(ctx, a) })
	})
}

func topicName(u *domain.HelpdeskUser) string {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = fmt.Sprintf("id%d", u.UserID)
	}
	if u.Username != "" {
		name += " (@" + u.Username + ")"
	}
	return truncRunes(name, 128)
}

func userHeader(u *domain.HelpdeskUser) string {
	var b strings.Builder
	fmt.Fprintf(&b, "👤 <b>%s</b>", html.EscapeString(u.Name))
	if u.Username != "" {
		fmt.Fprintf(&b, " (@%s)", html.EscapeString(u.Username))
	}
	fmt.Fprintf(&b, "\n🆔 <code>%d</code> · <a href=\"tg://user?id=%d\">профиль</a>", u.UserID, u.UserID)
	if u.LanguageCode != "" {
		fmt.Fprintf(&b, "\n🌐 Язык: %s", html.EscapeString(u.LanguageCode))
	}
	if u.Source != "" {
		fmt.Fprintf(&b, "\n🔗 Источник: <code>%s</code>", html.EscapeString(u.Source))
	}
	b.WriteString("\n\n<i>Всё, что вы пишете в этой теме, бот отправит пользователю от своего имени. " +
		"Заметки для коллег начинайте с // или ! — они не отправляются. " +
		"Ответ командой /1 на сообщение пользователя создаёт тикет.</i>")
	return b.String()
}

func humanDuration(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%d мин", max(m, 1))
	case m < 24*60:
		return fmt.Sprintf("%d ч %d мин", m/60, m%60)
	default:
		return fmt.Sprintf("%d дн %d ч", m/(24*60), (m/60)%24)
	}
}

func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
