// Package config loads service configuration from environment variables (optionally from a .env file).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
)

// Config is the full service configuration.
type Config struct {
	TelegramToken  string
	TelegramAPIURL string
	OwnerID        int64
	OwnerAbout     string

	// Socks5Addr, when set (e.g. "127.0.0.1:1080"), routes every outbound call this service makes —
	// Telegram Bot API and all LLM providers — through that unauthenticated local SOCKS5 proxy.
	// For hosts where some or all of these are blocked/censored directly but a local bypass
	// (Xray/V2Ray etc.) already reaches them. Never applied to the Mini App's own inbound HTTP server.
	Socks5Addr string

	DBPath        string
	Location      *time.Location
	LogLevel      slog.Level
	LogFormat     string
	RetentionDays int
	DeepLinks     bool

	// Defaults are the settings used until changed in the bot or Mini App. Defaults.AIChain is built
	// from the *_API_KEY variables (each may hold several comma-separated keys).
	Defaults domain.Settings

	DebounceMaxWait time.Duration
	ContextMessages int
	Workers         int
	// NoisePrefilter: skip the LLM call entirely for a batch that's unmistakably noise (bare
	// "спасибо"/"ок"/emoji reaction) — saves tokens/quota on the highest-volume, lowest-value
	// traffic. See internal/service/noisefilter.go for exactly what qualifies.
	NoisePrefilter bool

	AITimeout    time.Duration
	AIMaxRetries int

	AnthropicBaseURL string
	ClaudeEffort     string
	ClaudeFallbacks  bool
	ClaudeMaxTokens  int
	ClaudePresets    []string

	GeminiBaseURL   string
	GeminiMaxTokens int
	GeminiPresets   []string

	GroqBaseURL   string
	GroqMaxTokens int
	GroqPresets   []string

	MistralBaseURL   string
	MistralMaxTokens int
	MistralPresets   []string

	OpenRouterBaseURL   string
	OpenRouterMaxTokens int
	OpenRouterPresets   []string

	// WebAppAddr is where the Mini App HTTP server (task manager + settings UI) listens.
	WebAppAddr string
	// WebAppDevInsecure lets requests without valid Telegram initData through when they come
	// from a private/loopback address — for opening the Mini App from a LAN browser before a
	// public HTTPS domain is wired up. Never set on an internet-reachable deployment.
	WebAppDevInsecure bool
	// WebAppPublicURL, once set to an https:// address (e.g. after a Cloudflare Tunnel or
	// reverse proxy is in place), makes the bot show a "🌐 Открыть веб-панель" button. Empty by
	// default — no button until the Mini App is actually reachable from Telegram.
	WebAppPublicURL string
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

// Load reads and validates configuration from the environment.
func Load() (*Config, error) {
	var errs []error
	c := &Config{
		TelegramToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramAPIURL:    str("TELEGRAM_API_URL", "https://api.telegram.org"),
		Socks5Addr:        str("SOCKS5_PROXY", ""),
		OwnerAbout:        os.Getenv("OWNER_ABOUT"),
		DBPath:            str("DB_PATH", "/var/lib/tg-triage/tgtriage.db"),
		LogFormat:         strings.ToLower(str("LOG_FORMAT", "json")),
		AnthropicBaseURL:  str("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		ClaudeEffort:      str("CLAUDE_EFFORT", "low"),
		GeminiBaseURL:     str("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com"),
		GroqBaseURL:       str("GROQ_BASE_URL", "https://api.groq.com/openai/v1"),
		MistralBaseURL:    str("MISTRAL_BASE_URL", "https://api.mistral.ai/v1"),
		OpenRouterBaseURL: str("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
		ClaudePresets:     list("CLAUDE_MODEL_PRESETS", "claude-opus-5,claude-sonnet-5,claude-haiku-4-5"),
		GeminiPresets:     list("GEMINI_MODEL_PRESETS", "gemini-3.6-flash,gemini-3.8-flash,gemini-3.1-pro-preview"),
		GroqPresets:       list("GROQ_MODEL_PRESETS", "openai/gpt-oss-120b,openai/gpt-oss-20b,qwen/qwen3-32b"),
		MistralPresets:    list("MISTRAL_MODEL_PRESETS", "mistral-small-latest,mistral-large-latest,open-mistral-nemo"),
		OpenRouterPresets: list("OPENROUTER_MODEL_PRESETS", "openrouter/free,google/gemma-4-31b-it:free,nvidia/nemotron-3-super-120b-a12b:free"),
	}
	if c.TelegramToken == "" {
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN is required"))
	}
	owner, err := strconv.ParseInt(os.Getenv("OWNER_ID"), 10, 64)
	if err != nil || owner <= 0 {
		errs = append(errs, errors.New("OWNER_ID must be a positive Telegram user id"))
	}
	c.OwnerID = owner

	tz := str("TIMEZONE", "Europe/Moscow")
	if c.Location, err = time.LoadLocation(tz); err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE: %w", err))
	}
	if err := c.LogLevel.UnmarshalText([]byte(str("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	intVar := func(key string, def, minV, maxV int) int {
		v, err := strconv.Atoi(str(key, strconv.Itoa(def)))
		if err != nil || v < minV || v > maxV {
			errs = append(errs, fmt.Errorf("%s must be an integer within %d..%d", key, minV, maxV))
			return def
		}
		return v
	}
	boolVar := func(key string, def bool) bool {
		v, err := strconv.ParseBool(str(key, strconv.FormatBool(def)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s must be a boolean", key))
			return def
		}
		return v
	}
	durVar := func(key string, def time.Duration) time.Duration {
		v, err := time.ParseDuration(str(key, def.String()))
		if err != nil || v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive duration (e.g. 90s)", key))
			return def
		}
		return v
	}

	c.RetentionDays = intVar("MESSAGE_RETENTION_DAYS", 90, 0, 3650)
	c.DeepLinks = boolVar("DEEP_LINKS", true)
	c.DebounceMaxWait = time.Duration(intVar("DEBOUNCE_MAX_WAIT_SECONDS", 120, 1, 3600)) * time.Second
	c.ContextMessages = intVar("CONTEXT_MESSAGES", 12, 0, 100)
	c.NoisePrefilter = boolVar("NOISE_PREFILTER_ENABLED", true)
	c.Workers = intVar("ANALYSIS_WORKERS", 2, 1, 16)
	c.AITimeout = durVar("AI_TIMEOUT", 90*time.Second)
	c.AIMaxRetries = intVar("AI_MAX_RETRIES", 3, 0, 10)
	c.ClaudeFallbacks = boolVar("CLAUDE_FALLBACKS", true)
	c.ClaudeMaxTokens = intVar("CLAUDE_MAX_TOKENS", 16000, 1024, 64000)
	c.GeminiMaxTokens = intVar("GEMINI_MAX_TOKENS", 8192, 1024, 65536)
	c.GroqMaxTokens = intVar("GROQ_MAX_TOKENS", 8192, 1024, 65536)
	c.MistralMaxTokens = intVar("MISTRAL_MAX_TOKENS", 8192, 1024, 65536)
	c.OpenRouterMaxTokens = intVar("OPENROUTER_MAX_TOKENS", 8192, 1024, 65536)

	c.WebAppAddr = str("WEBAPP_ADDR", ":8080")
	c.WebAppDevInsecure = boolVar("WEBAPP_DEV_INSECURE", false)
	c.WebAppPublicURL = strings.TrimSuffix(str("WEBAPP_PUBLIC_URL", ""), "/")
	if c.WebAppPublicURL != "" && !strings.HasPrefix(c.WebAppPublicURL, "https://") {
		errs = append(errs, errors.New("WEBAPP_PUBLIC_URL must start with https:// (Telegram refuses Mini Apps over plain http)"))
	}

	switch c.ClaudeEffort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		errs = append(errs, errors.New("CLAUDE_EFFORT must be one of low|medium|high|xhigh|max or empty"))
	}

	sens, ok := domain.ParseSensitivity(str("SENSITIVITY", "medium"))
	if !ok {
		errs = append(errs, errors.New("SENSITIVITY must be low|medium|high"))
	}
	primary := strings.ToLower(str("AI_PROVIDER", domain.ProviderClaude))
	if !domain.KnownProvider(primary) {
		errs = append(errs, errors.New("AI_PROVIDER must be one of claude, gemini, groq, mistral, openrouter"))
	}
	c.Defaults = domain.Settings{
		AIChain: defaultChain(primary, map[string][]string{
			domain.ProviderClaude:     list("ANTHROPIC_API_KEY", ""),
			domain.ProviderGemini:     list("GEMINI_API_KEY", ""),
			domain.ProviderGroq:       list("GROQ_API_KEY", ""),
			domain.ProviderMistral:    list("MISTRAL_API_KEY", ""),
			domain.ProviderOpenRouter: list("OPENROUTER_API_KEY", ""),
		}),
		ClaudeModel:       str("CLAUDE_MODEL", "claude-opus-5"),
		GeminiModel:       str("GEMINI_MODEL", "gemini-3.6-flash"),
		GroqModel:         str("GROQ_MODEL", "openai/gpt-oss-120b"),
		MistralModel:      str("MISTRAL_MODEL", "mistral-small-latest"),
		OpenRouterModel:   str("OPENROUTER_MODEL", "openrouter/free"),
		DebounceSeconds:   intVar("DEBOUNCE_SECONDS", 20, 1, 600),
		Sensitivity:       sens,
		DigestEnabled:     boolVar("DIGEST_ENABLED", true),
		DigestTime:        str("DIGEST_TIME", "09:00"),
		TriagePaused:      false,
		MarkReadOnWork:    boolVar("MARK_READ_ON_WORK", true),
		NotifyDoneOnClose: boolVar("NOTIFY_DONE_ON_CLOSE", false),
	}
	if !validClock(c.Defaults.DigestTime) {
		errs = append(errs, errors.New("DIGEST_TIME must be HH:MM"))
	}
	return c, errors.Join(errs...)
}

// defaultChain orders the env keys into the initial AI chain: AI_PROVIDER's keys first, then the
// other providers in domain.Providers order, each provider's keys in the order they were listed.
func defaultChain(primary string, keys map[string][]string) []domain.AIKey {
	var chain []domain.AIKey
	for i, p := range append([]string{primary}, domain.Providers...) {
		if i > 0 && p == primary {
			continue
		}
		for _, k := range keys[p] {
			chain = append(chain, domain.AIKey{Provider: p, Key: k})
		}
	}
	return chain
}

func str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func list(key, def string) []string {
	var out []string
	for _, p := range strings.Split(str(key, def), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validClock(s string) bool {
	t, err := time.Parse("15:04", s)
	return err == nil && t.Format("15:04") == s
}
