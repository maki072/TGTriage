// Package config loads the bootstrap configuration from environment variables (optionally from a .env
// file). Everything else — AI keys, models, triage, helpdesk, backups — lives in the database and is
// edited in the Mini App; legacy variables for those settings are imported into the DB once on start.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config is the bootstrap configuration: what the service needs before it can open the DB and
// authenticate the Mini App.
type Config struct {
	TelegramToken  string
	TelegramAPIURL string
	OwnerID        int64

	// Socks5Addr, when set (e.g. "127.0.0.1:1080"), routes every outbound call this service makes —
	// Telegram Bot API and all LLM providers — through that unauthenticated local SOCKS5 proxy.
	// Never applied to the Mini App's own inbound HTTP server.
	Socks5Addr string

	DBPath    string
	LogLevel  slog.Level
	LogFormat string

	// WebAppAddr is where the Mini App HTTP server listens.
	WebAppAddr string
	// WebAppDevInsecure lets requests without valid Telegram initData through when they come
	// from a private/loopback address. Never set on an internet-reachable deployment.
	WebAppDevInsecure bool
}

// LoadEnvFile loads KEY=VALUE pairs into the process environment without overriding existing variables.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// Load reads and validates the bootstrap configuration from the environment.
func Load() (*Config, error) {
	var errs []error
	c := &Config{
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAPIURL: str("TELEGRAM_API_URL", "https://api.telegram.org"),
		Socks5Addr:     str("SOCKS5_PROXY", ""),
		DBPath:         str("DB_PATH", "/var/lib/tg-triage/tgtriage.db"),
		LogFormat:      strings.ToLower(str("LOG_FORMAT", "json")),
		WebAppAddr:     str("WEBAPP_ADDR", ":8080"),
	}
	if c.TelegramToken == "" {
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN is required"))
	}
	owner, err := strconv.ParseInt(os.Getenv("OWNER_ID"), 10, 64)
	if err != nil || owner <= 0 {
		errs = append(errs, errors.New("OWNER_ID must be a positive Telegram user id"))
	}
	c.OwnerID = owner
	if err := c.LogLevel.UnmarshalText([]byte(str("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	if c.WebAppDevInsecure, err = strconv.ParseBool(str("WEBAPP_DEV_INSECURE", "false")); err != nil {
		errs = append(errs, errors.New("WEBAPP_DEV_INSECURE must be a boolean"))
	}
	return c, errors.Join(errs...)
}

func str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}
