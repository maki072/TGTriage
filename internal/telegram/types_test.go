package telegram

import (
	"encoding/json"
	"testing"
)

func TestMessageOriginAuthorName(t *testing.T) {
	cases := []struct {
		name string
		o    MessageOrigin
		want string
	}{
		{"user", MessageOrigin{Type: "user", SenderUser: &User{ID: 1, FirstName: "Иван", LastName: "Петров"}}, "Иван Петров"},
		{"hidden user", MessageOrigin{Type: "hidden_user", SenderUserName: "Аноним"}, "Аноним"},
		{"channel with signature", MessageOrigin{Type: "channel", Chat: &Chat{Title: "Новости"}, AuthorSignature: "Мария"}, "Новости (Мария)"},
		{"anonymous group admin", MessageOrigin{Type: "chat", SenderChat: &Chat{Title: "Команда"}}, "Команда"},
		{"nothing known", MessageOrigin{Type: "hidden_user"}, "неизвестный автор"},
	}
	for _, c := range cases {
		if got := c.o.AuthorName(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDecodeForwardedMessage(t *testing.T) {
	raw := `{"message_id":5,"chat":{"id":1,"type":"private"},"date":200,"text":"hi",
		"forward_origin":{"type":"user","date":100,"sender_user":{"id":7,"is_bot":false,"first_name":"A","username":"a"}}}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	o := m.ForwardOrigin
	if o == nil || o.Date != 100 || o.SenderUser == nil || o.SenderUser.ID != 7 {
		t.Fatalf("forward_origin not decoded: %+v", o)
	}
}
