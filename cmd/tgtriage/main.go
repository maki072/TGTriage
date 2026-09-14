// Command tgtriage runs the Telegram Business AI assistant and task manager.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embedded tz database: TIMEZONE works even on minimal systems

	"tgtriage/internal/ai"
	"tgtriage/internal/ai/claude"
	"tgtriage/internal/ai/gemini"
	"tgtriage/internal/ai/openaicompat"
	"tgtriage/internal/config"
	tgbot "tgtriage/internal/delivery/telegram"
	"tgtriage/internal/delivery/webapp"
	"tgtriage/internal/domain"
	"tgtriage/internal/repository/sqlite"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

var version = "dev"

func main() {
	envFile := flag.String("env", "", "path to an env file (KEY=VALUE); existing environment variables take precedence")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *envFile != "" {
		if err := config.LoadEnvFile(*envFile); err != nil {
			fmt.Fprintln(os.Stderr, "load env file:", err)
			os.Exit(2)
		}
	} else if _, err := os.Stat(".env"); err == nil {
		if err := config.LoadEnvFile(".env"); err != nil {
			fmt.Fprintln(os.Stderr, "load .env:", err)
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:\n"+err.Error())
		os.Exit(2)
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func run(cfg *config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Error("close database", "err", err)
		}
	}()

	registry := ai.NewRegistry()
	if cfg.AnthropicAPIKey != "" {
		registry.Register(claude.New(claude.Config{
			APIKey:     cfg.AnthropicAPIKey,
			BaseURL:    cfg.AnthropicBaseURL,
			Effort:     cfg.ClaudeEffort,
			Fallbacks:  cfg.ClaudeFallbacks,
			MaxTokens:  cfg.ClaudeMaxTokens,
			Timeout:    cfg.AITimeout,
			Socks5Addr: cfg.Socks5Addr,
		}))
	}
	if cfg.GeminiAPIKey != "" {
		registry.Register(gemini.New(gemini.Config{
			APIKey:      cfg.GeminiAPIKey,
			BaseURL:     cfg.GeminiBaseURL,
			MaxTokens:   cfg.GeminiMaxTokens,
			Temperature: 0.2,
			Timeout:     cfg.AITimeout,
			Socks5Addr:  cfg.Socks5Addr,
		}))
	}
	// Groq, Mistral and OpenRouter all speak the same OpenAI-compatible Chat Completions wire
	// format (json_schema structured outputs, Bearer auth) — one adapter, three configurations.
	if cfg.GroqAPIKey != "" {
		registry.Register(openaicompat.New(openaicompat.Config{
			Name: domain.ProviderGroq, APIKey: cfg.GroqAPIKey, BaseURL: cfg.GroqBaseURL,
			MaxTokens: cfg.GroqMaxTokens, Temperature: 0.2, Timeout: cfg.AITimeout, Socks5Addr: cfg.Socks5Addr,
		}))
	}
	if cfg.MistralAPIKey != "" {
		registry.Register(openaicompat.New(openaicompat.Config{
			Name: domain.ProviderMistral, APIKey: cfg.MistralAPIKey, BaseURL: cfg.MistralBaseURL,
			MaxTokens: cfg.MistralMaxTokens, Temperature: 0.2, Timeout: cfg.AITimeout, Socks5Addr: cfg.Socks5Addr,
		}))
	}
	if cfg.OpenRouterAPIKey != "" {
		// OpenRouter attribution headers (optional but recommended by their docs); Referer only
		// makes sense once the Mini App has a real public URL.
		headers := map[string]string{"X-Title": "tg-triage"}
		if cfg.WebAppPublicURL != "" {
			headers["HTTP-Referer"] = cfg.WebAppPublicURL
		}
		registry.Register(openaicompat.New(openaicompat.Config{
			Name: domain.ProviderOpenRouter, APIKey: cfg.OpenRouterAPIKey, BaseURL: cfg.OpenRouterBaseURL,
			MaxTokens: cfg.OpenRouterMaxTokens, Temperature: 0.2, Timeout: cfg.AITimeout, Socks5Addr: cfg.Socks5Addr,
			Headers: headers,
		}))
	}

	settings, err := service.NewSettingsService(ctx, store.Settings, cfg.Defaults, registry.Names())
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	api := telegram.NewClient(cfg.TelegramToken, cfg.TelegramAPIURL, cfg.Socks5Addr, log.With("component", "telegram"))
	me, err := api.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	log.Info("bot authorized", "username", me.Username, "bot_id", me.ID)

	conns := service.NewConnectionService(store.Connections, cfg.OwnerID)
	tasks := service.NewTaskService(store.Tasks, store.Messages, store.Analyses, conns, settings,
		tgbot.NewGateway(api), cfg.Location, log)
	bot := tgbot.New(api, tgbot.Config{
		OwnerID:           cfg.OwnerID,
		Location:          cfg.Location,
		ClaudePresets:     cfg.ClaudePresets,
		GeminiPresets:     cfg.GeminiPresets,
		GroqPresets:       cfg.GroqPresets,
		MistralPresets:    cfg.MistralPresets,
		OpenRouterPresets: cfg.OpenRouterPresets,
		DeepLinks:         cfg.DeepLinks,
		WebAppURL:         cfg.WebAppPublicURL,
	}, tasks, settings, conns, log)
	triage := service.NewTriageService(service.TriageConfig{
		MaxWait:         cfg.DebounceMaxWait,
		ContextMessages: cfg.ContextMessages,
		Workers:         cfg.Workers,
		MaxRetries:      cfg.AIMaxRetries,
		CallTimeout:     cfg.AITimeout,
		OwnerAbout:      cfg.OwnerAbout,
		Location:        cfg.Location,
		NoisePrefilter:  cfg.NoisePrefilter,
	}, store.Messages, store.Tasks, store.Analyses, conns, settings, registry, bot, log)
	bot.SetTriage(triage)
	scheduler := service.NewScheduler(tasks, settings, store.Messages, bot, cfg.Location,
		time.Duration(cfg.RetentionDays)*24*time.Hour, log)
	webappSrv := webapp.New(webapp.Config{
		Addr: cfg.WebAppAddr, BotToken: cfg.TelegramToken, OwnerID: cfg.OwnerID, Location: cfg.Location,
		ClaudePresets: cfg.ClaudePresets, GeminiPresets: cfg.GeminiPresets, GroqPresets: cfg.GroqPresets,
		MistralPresets: cfg.MistralPresets, OpenRouterPresets: cfg.OpenRouterPresets,
		DevInsecure: cfg.WebAppDevInsecure,
	}, tasks, settings, conns, triage, log)

	if err := triage.Start(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Go(func() { scheduler.Run(ctx) })
	wg.Go(func() {
		if err := webappSrv.Run(ctx); err != nil {
			log.Error("webapp server stopped", "err", err)
		}
	})

	st := settings.Get()
	log.Info("service started", "version", version, "providers", registry.Names(),
		"active_provider", st.ActiveProvider, "model", st.ActiveModel(), "timezone", cfg.Location.String(),
		"webapp_addr", cfg.WebAppAddr, "webapp_dev_insecure", cfg.WebAppDevInsecure, "webapp_public_url", cfg.WebAppPublicURL)

	bot.Run(ctx) // blocks until SIGINT/SIGTERM

	log.Info("shutting down")
	if !triage.Wait(25 * time.Second) {
		log.Warn("triage workers did not finish in time; unanalyzed messages will be recovered on next start")
	}
	wg.Wait()
	log.Info("stopped")
	return nil
}
