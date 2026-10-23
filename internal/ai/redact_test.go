package ai

import (
	"strings"
	"testing"
)

func TestRedactHidesSecrets(t *testing.T) {
	secrets := []struct{ name, in, leak string }{
		{"password label", "логин: ivan.p пароль: Zx9!kLm2", "Zx9!kLm2"},
		{"login label", "логин: ivan.p пароль: Zx9!kLm2", "ivan.p"},
		{"password dash", "Пароль - Qwerty123", "Qwerty123"},
		{"password space", "пароль Qwerty123 не подходит", "Qwerty123"},
		{"login space", "логин olever.test", "olever.test"},
		{"token eq", "token=abcDEF123456", "abcDEF123456"},
		{"anthropic key", "вот ключ sk-ant-api03-AbCdEfGhIjKlMnOp", "AbCdEfGhIjKlMnOp"},
		{"google key", "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q", "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"},
		{"telegram token", "бот 123456789:AAFabcdefghijklmnopqrstuvwxyz0123456", "AAFabcdefghijklmnopqrstuvwxyz0123456"},
		{"jwt", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N", "dozjgNryP4J3jVmNHl0w5N"},
		{"url creds", "подключись к https://admin:S3cret@srv.example.com/db", "S3cret"},
		{"url query", "https://x.ru/api?token=0a1b2c3d4e5f&x=1", "0a1b2c3d4e5f"},
		{"anydesk code", "Подключиться к ПК по коду 409627158", "409627158"},
		{"anydesk grouped", "anydesk 409 627 158", "409 627 158"},
		{"email login", "по данным OLEVER.m6381@moiofis", "OLEVER.m6381"},
		{"email", "пиши на ivan@example.com", "ivan@example.com"},
		{"card", "карта 4111 1111 1111 1111", "4111 1111 1111 1111"},
		{"long token", "ключ доступа 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "9f86d081884c7d659a2feaa0c55ad015"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----", "MIIEowIBAAKCAQEA"},
		{"key colon", "key: Zk39sd", "Zk39sd"},
	}
	for _, c := range secrets {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); strings.Contains(got, c.leak) {
				t.Errorf("secret %q leaked: %q", c.leak, got)
			}
		})
	}
}

func TestRedactKeepsOrdinaryText(t *testing.T) {
	for _, s := range []string{
		"Привет! Не работает вход в админку, отдаёт 500.",
		"Пароль не подошёл, что делать?",
		"логин не работает",
		"Можешь глянуть до 15:30? Счёт №1234 на 12 500 руб.",
		"[19.10 12:18] USER: Здравствуйте, нужна помощь",
		"- id=12 [high/new] Починить 500 — контекст",
		"https://tg.016.help/panel/tasks/12",
		"позвони @ivan_petrov завтра",
		"CURRENT_TIME: 2026-10-23 16:19 (пятница)",
	} {
		if got := Redact(s); got != s {
			t.Errorf("ordinary text changed:\n in: %q\nout: %q", s, got)
		}
	}
}

func TestRedactIdempotent(t *testing.T) {
	in := "логин: ivan пароль: Zx9!kLm2 код 409627158 mail a@b.ru"
	once := Redact(in)
	if twice := Redact(once); twice != once {
		t.Errorf("not idempotent:\n%q\n%q", once, twice)
	}
}

func TestRedactMessageBareSecret(t *testing.T) {
	for _, s := range []string{"Qwerty123", "  Zx9!kLm2  ", "p@ssw0rd!"} {
		if got := RedactMessage(s); got != Mask {
			t.Errorf("RedactMessage(%q) = %q, want mask", s, got)
		}
	}
	for _, s := range []string{"Привет", "спасибо", "Здравствуйте", "https://example.com/Abc123", "Hello!", "ok ok"} {
		if got := RedactMessage(s); got != s {
			t.Errorf("RedactMessage(%q) = %q, want unchanged", s, got)
		}
	}
}
