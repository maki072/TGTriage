package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/repository/sqlite"
)

const testGroup = int64(-1001)
const testBotID = int64(999)

type copyCall struct {
	from    int64
	ids     []int
	to      int64
	topic   int
	replyTo int
}

type sentCall struct {
	chat    int64
	topic   int
	text    string
	replyTo int
}

type fakeTransport struct {
	mu       sync.Mutex
	next     int
	topics   []string
	copies   []copyCall
	sent     []sentCall
	edits    []string
	reopened []int
	blocked  map[int64]bool
	gone     map[int]bool
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{next: 100, blocked: map[int64]bool{}, gone: map[int]bool{}}
}

func (f *fakeTransport) id() int { f.next++; return f.next }

func (f *fakeTransport) CreateTopic(_ context.Context, _ int64, name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topics = append(f.topics, name)
	return f.id(), nil
}

func (f *fakeTransport) EditTopic(context.Context, int64, int, string) error { return nil }
func (f *fakeTransport) CloseTopic(context.Context, int64, int) error        { return nil }

func (f *fakeTransport) ReopenTopic(_ context.Context, _ int64, topic int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reopened = append(f.reopened, topic)
	return nil
}

func (f *fakeTransport) Copy(_ context.Context, from int64, ids []int, to int64, topic, replyTo int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked[to] {
		return nil, fmt.Errorf("%w: 403", domain.ErrUserBlocked)
	}
	if f.gone[topic] {
		return nil, fmt.Errorf("%w: thread not found", domain.ErrTopicGone)
	}
	f.copies = append(f.copies, copyCall{from, ids, to, topic, replyTo})
	out := make([]int, len(ids))
	for i := range ids {
		out[i] = f.id()
	}
	return out, nil
}

func (f *fakeTransport) SendHTML(_ context.Context, chat int64, topic int, text string, replyTo int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked[chat] {
		return 0, fmt.Errorf("%w: 403", domain.ErrUserBlocked)
	}
	f.sent = append(f.sent, sentCall{chat, topic, text, replyTo})
	return f.id(), nil
}

func (f *fakeTransport) EditText(_ context.Context, chat int64, msg int, text string, _ json.RawMessage, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, fmt.Sprintf("%d/%d:%s", chat, msg, text))
	return nil
}

func (f *fakeTransport) DeleteMessage(context.Context, int64, int) error { return nil }

func (f *fakeTransport) IsChatMember(_ context.Context, _ int64, user int64) (bool, error) {
	return user == 500, nil
}

func (f *fakeTransport) CheckGroup(context.Context, int64) (*GroupCheck, error) {
	return &GroupCheck{}, nil
}

func (f *fakeTransport) sentTo(chat int64) []sentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentCall
	for _, s := range f.sent {
		if s.chat == chat {
			out = append(out, s)
		}
	}
	return out
}

type helpdeskFixture struct {
	svc      *HelpdeskService
	tr       *fakeTransport
	store    *sqlite.Store
	settings *SettingsService
}

func newHelpdeskFixture(t *testing.T, env map[string]string) *helpdeskFixture {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "hd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := map[string]string{"HELPDESK_ENABLED": "true", "HELPDESK_GROUP_ID": fmt.Sprint(testGroup)}
	for k, v := range env {
		base[k] = v
	}
	settings := newTestSettings(t, newMemSettingsRepo(), base)
	tr := newFakeTransport()
	svc := NewHelpdeskService(store.Helpdesk, store.Messages, settings, GlobalHelpdeskConfig(settings), tr, 1, testBotID, 0,
		slog.New(slog.DiscardHandler))
	return &helpdeskFixture{svc: svc, tr: tr, store: store, settings: settings}
}

func (fx *helpdeskFixture) userSays(t *testing.T, msgID int, text string) {
	t.Helper()
	if err := fx.svc.OnUserMessage(context.Background(), UserMessage{UserID: 42, Name: "Иван", Username: "ivan",
		MessageID: msgID, Text: text, Date: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestUserMessageCreatesTopicAndRelays(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)

	if err := fx.svc.OnUserMessage(ctx, UserMessage{UserID: 42, Name: "Иван", Username: "ivan", Start: true, StartParam: "site"}); err != nil {
		t.Fatal(err)
	}
	if got := fx.tr.sentTo(42); len(got) != 1 || !strings.Contains(got[0].text, "Здравствуйте") {
		t.Fatalf("greeting expected on /start: %+v", got)
	}
	if len(fx.tr.topics) != 0 {
		t.Fatal("/start alone must not create a topic")
	}

	fx.userSays(t, 10, "Не работает оплата")
	fx.userSays(t, 11, "Ошибка 500")
	if len(fx.tr.topics) != 1 || fx.tr.topics[0] != "Иван (@ivan)" {
		t.Fatalf("exactly one topic named after the user expected: %v", fx.tr.topics)
	}
	u, err := fx.svc.User(ctx, 42)
	if err != nil || u.TopicID == 0 || u.Source != "site" || u.AwaitingSince == nil {
		t.Fatalf("user state: %+v err=%v", u, err)
	}
	if header := fx.tr.sentTo(testGroup); len(header) != 1 || !strings.Contains(header[0].text, "Источник") {
		t.Errorf("topic header with the source expected: %+v", header)
	}
	if len(fx.tr.copies) != 2 || fx.tr.copies[0].to != testGroup || fx.tr.copies[0].topic != u.TopicID {
		t.Errorf("messages must be copied into the topic: %+v", fx.tr.copies)
	}
	msgs, _ := fx.svc.Conversation(ctx, 42, 10)
	if len(msgs) != 3 || !msgs[0].Outgoing || msgs[1].Text != "Не работает оплата" || msgs[2].Outgoing {
		t.Errorf("conversation (greeting + two messages) must be stored: %+v", msgs)
	}
}

func TestOperatorReplyRelaysAndClearsAwaiting(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)
	fx.userSays(t, 10, "Не работает оплата")
	u, _ := fx.svc.User(ctx, 42)
	copyID := 0
	if m, err := fx.store.Helpdesk.MessageByUserMsg(ctx, 0, 42, 10); err == nil {
		copyID = m.GroupMsgID
	}

	note := OperatorMessage{GroupID: testGroup, TopicID: u.TopicID, MessageID: 300, OperatorID: 500, RawText: "// проверю логи", Text: "// проверю логи"}
	if err := fx.svc.OnOperatorMessage(ctx, note); err != nil {
		t.Fatal(err)
	}
	if len(fx.tr.copies) != 1 {
		t.Fatal("an internal note must not reach the user")
	}

	reply := OperatorMessage{GroupID: testGroup, TopicID: u.TopicID, MessageID: 301, ReplyToID: copyID, OperatorID: 500,
		RawText: "Уже смотрим", Text: "Уже смотрим"}
	if err := fx.svc.OnOperatorMessage(ctx, reply); err != nil {
		t.Fatal(err)
	}
	last := fx.tr.copies[len(fx.tr.copies)-1]
	if last.from != testGroup || last.to != 42 || last.replyTo != 10 {
		t.Errorf("operator message must be copied to the user as a reply to their message: %+v", last)
	}
	if u, _ = fx.svc.User(ctx, 42); u.AwaitingSince != nil {
		t.Error("an operator answer must clear the waiting state")
	}

	// the user replies to the operator's copy: the topic copy must reply to the operator's message
	outCopy, _ := fx.store.Helpdesk.MessageByGroupMsg(ctx, 0, testGroup, 301)
	if err := fx.svc.OnUserMessage(ctx, UserMessage{UserID: 42, Name: "Иван", Username: "ivan", MessageID: 12,
		ReplyToID: outCopy.UserMsgID, Text: "Спасибо", Date: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if last = fx.tr.copies[len(fx.tr.copies)-1]; last.replyTo != 301 {
		t.Errorf("user reply must thread to the operator's message in the topic: %+v", last)
	}

	if err := fx.svc.OnOperatorEdited(ctx, testGroup, 301, "Уже починили", nil, false, "Уже починили"); err != nil {
		t.Fatal(err)
	}
	if len(fx.tr.edits) != 1 || !strings.HasPrefix(fx.tr.edits[0], fmt.Sprintf("42/%d:", outCopy.UserMsgID)) {
		t.Errorf("operator edit must be mirrored to the user's copy: %v", fx.tr.edits)
	}
}

func TestDeliveryFailureIsReportedInTopic(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)
	fx.userSays(t, 10, "Помогите")
	u, _ := fx.svc.User(ctx, 42)
	fx.tr.blocked[42] = true
	if err := fx.svc.OnOperatorMessage(ctx, OperatorMessage{GroupID: testGroup, TopicID: u.TopicID, MessageID: 400,
		OperatorID: 500, RawText: "Здравствуйте", Text: "Здравствуйте"}); err != nil {
		t.Fatal(err)
	}
	notices := fx.tr.sentTo(testGroup)
	if n := notices[len(notices)-1]; !strings.Contains(n.text, "заблокировал") || n.replyTo != 400 {
		t.Errorf("delivery failure notice expected as a reply to the operator: %+v", n)
	}
	if u, _ = fx.svc.User(ctx, 42); !u.Blocked {
		t.Error("user must be marked as blocked")
	}
}

func TestDeletedTopicIsRecreated(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)
	fx.userSays(t, 10, "Первое")
	u, _ := fx.svc.User(ctx, 42)
	fx.tr.gone[u.TopicID] = true
	fx.userSays(t, 11, "Второе")
	u2, _ := fx.svc.User(ctx, 42)
	if len(fx.tr.topics) != 2 || u2.TopicID == u.TopicID {
		t.Fatalf("a deleted topic must be recreated: topics=%v old=%d new=%d", fx.tr.topics, u.TopicID, u2.TopicID)
	}
	if last := fx.tr.copies[len(fx.tr.copies)-1]; last.topic != u2.TopicID {
		t.Errorf("message must land in the new topic: %+v", last)
	}
}

func TestOffHoursAutoReply(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)
	if _, err := fx.settings.Update(ctx, func(s *domain.Settings) {
		s.Helpdesk.HoursEnabled, s.Helpdesk.HoursStart, s.Helpdesk.HoursEnd = true, "00:00", "00:00" // never open
	}); err != nil {
		t.Fatal(err)
	}
	fx.userSays(t, 10, "Есть кто?")
	fx.userSays(t, 11, "Ау")
	toUser := fx.tr.sentTo(42)
	if len(toUser) != 1 || !strings.Contains(toUser[0].text, "нерабочее время") || !strings.Contains(toUser[0].text, "пн–пт 00:00–00:00") {
		t.Fatalf("one off-hours reply per waiting cycle expected: %+v", toUser)
	}
	mirrored := false
	for _, s := range fx.tr.sentTo(testGroup) {
		mirrored = mirrored || strings.Contains(s.text, "Автоответ")
	}
	if !mirrored {
		t.Error("auto-reply must be mirrored into the topic")
	}
}

func TestRemindersRepeatAfterInterval(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)
	fx.userSays(t, 10, "Жду ответа")
	u, _ := fx.svc.User(ctx, 42)
	past := time.Now().Add(-20 * time.Minute)
	u.AwaitingSince = &past
	if err := fx.store.Helpdesk.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	before := len(fx.tr.sentTo(testGroup))
	fx.svc.CheckReminders(ctx)
	fx.svc.CheckReminders(ctx)
	got := fx.tr.sentTo(testGroup)[before:]
	if len(got) != 1 || !strings.Contains(got[0].text, "ждёт ответа 20 мин") || got[0].topic != u.TopicID {
		t.Errorf("exactly one reminder expected until the interval passes again: %+v", got)
	}
}

func TestResolveForwardFromTopicCopy(t *testing.T) {
	ctx := context.Background()
	fx := newHelpdeskFixture(t, nil)
	fx.userSays(t, 10, "Не приходит код")
	fx.userSays(t, 11, "Уже 10 минут жду")

	u, m := fx.svc.ResolveForward(ctx, ForwardedMessage{OriginUserID: testBotID, OriginDate: time.Now(), Text: "Уже 10 минут жду"})
	if u == nil || u.UserID != 42 || m.MessageID != 11 {
		t.Fatalf("a copy forwarded from the topic must resolve to its user and message: %+v %+v", u, m)
	}
	if u, _ := fx.svc.ResolveForward(ctx, ForwardedMessage{OriginUserID: 42, OriginDate: time.Now(), Text: "x"}); u == nil {
		t.Error("a message forwarded directly from the user must resolve")
	}
	if u, _ := fx.svc.ResolveForward(ctx, ForwardedMessage{OriginUserID: 7777, Text: "x"}); u != nil {
		t.Error("strangers must not resolve")
	}
}

func TestIsOperator(t *testing.T) {
	fx := newHelpdeskFixture(t, nil)
	ctx := context.Background()
	if !fx.svc.IsOperator(ctx, 1) || !fx.svc.IsOperator(ctx, 500) || fx.svc.IsOperator(ctx, 42) {
		t.Error("owner and group members are operators, others are not")
	}
}

func TestCommandsDetection(t *testing.T) {
	for raw, want := range map[string]bool{"/1": true, "/1@tgbot": true, " /1 ": true, "/10": false, "1": false} {
		if IsForceTicketCommand(raw) != want {
			t.Errorf("force ticket %q: want %v", raw, want)
		}
	}
	for raw, want := range map[string]bool{"// note": true, "!note": true, "http://x": false, "Привет!": false} {
		if IsInternalNote(raw) != want {
			t.Errorf("note %q: want %v", raw, want)
		}
	}
}
