// Command tgtriage runs the Telegram Business AI assistant, the support desk and the task manager.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // embedded tz database: the timezone setting works even on minimal systems

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

// errRestart asks main to exit with a non-zero code so systemd (Restart=on-failure) starts the service again.
var errRestart = errors.New("restart requested")

const restartExitCode = 75

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
		if errors.Is(err, errRestart) {
			log.Info("exiting for restart")
			os.Exit(restartExitCode)
		}
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

// providerSignature captures everything the LLM adapters are built from; a change rebuilds them.
func providerSignature(st domain.Settings) string {
	sig := fmt.Sprint(st.ClaudeEffort, st.ClaudeFallbacks, st.AITimeoutSec, st.WebAppPublicURL)
	for _, p := range domain.Providers {
		ps := st.Provider(p)
		sig += fmt.Sprint("|", p, ps.BaseURL, ps.MaxTokens)
	}
	return sig
}

// registerProviders (re)builds every adapter from settings. API keys are not bound to adapters: they
// come with each request from the AI chain.
func registerProviders(registry *ai.Registry, st domain.Settings, socks5 string) {
	timeout := time.Duration(st.AITimeoutSec) * time.Second
	registry.Register(claude.New(claude.Config{
		BaseURL: st.Claude.BaseURL, Effort: st.ClaudeEffort, Fallbacks: st.ClaudeFallbacks,
		MaxTokens: st.Claude.MaxTokens, Timeout: timeout, Socks5Addr: socks5,
	}))
	registry.Register(gemini.New(gemini.Config{
		BaseURL: st.Gemini.BaseURL, MaxTokens: st.Gemini.MaxTokens, Temperature: 0.2, Timeout: timeout, Socks5Addr: socks5,
	}))
	// Groq, Mistral and OpenRouter all speak the same OpenAI-compatible Chat Completions wire
	// format (json_schema structured outputs, Bearer auth) — one adapter, three configurations.
	registry.Register(openaicompat.New(openaicompat.Config{
		Name: domain.ProviderGroq, BaseURL: st.Groq.BaseURL,
		MaxTokens: st.Groq.MaxTokens, Temperature: 0.2, Timeout: timeout, Socks5Addr: socks5,
	}))
	registry.Register(openaicompat.New(openaicompat.Config{
		Name: domain.ProviderMistral, BaseURL: st.Mistral.BaseURL,
		MaxTokens: st.Mistral.MaxTokens, Temperature: 0.2, Timeout: timeout, Socks5Addr: socks5,
	}))
	// OpenRouter attribution headers (optional but recommended by their docs); Referer only
	// makes sense once the Mini App has a real public URL.
	headers := map[string]string{"X-Title": "tg-triage"}
	if st.WebAppPublicURL != "" {
		headers["HTTP-Referer"] = st.WebAppPublicURL
	}
	registry.Register(openaicompat.New(openaicompat.Config{
		Name: domain.ProviderOpenRouter, BaseURL: st.OpenRouter.BaseURL,
		MaxTokens: st.OpenRouter.MaxTokens, Temperature: 0.2, Timeout: timeout, Socks5Addr: socks5,
		Headers: headers,
	}))
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

	settings, err := service.NewSettingsService(ctx, store.Settings, os.LookupEnv, log)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	botsSvc, err := service.NewBotService(ctx, store.Bots, log)
	if err != nil {
		return fmt.Errorf("load bots: %w", err)
	}
	registry := ai.NewRegistry()
	registerProviders(registry, settings.Get(), cfg.Socks5Addr)

	api := telegram.NewClient(cfg.TelegramToken, cfg.TelegramAPIURL, cfg.Socks5Addr, log.With("component", "telegram"))
	me, err := api.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	log.Info("bot authorized", "username", me.Username, "bot_id", me.ID, "main_web_app", me.HasMainWebApp)

	conns := service.NewConnectionService(store.Connections, cfg.OwnerID)
	gateway := tgbot.NewGateway(api, cfg.OwnerID, me.ID)
	tasks := service.NewTaskService(store.Tasks, store.Messages, store.Analyses, conns, settings, gateway, log)
	helpdesk := service.NewHelpdeskService(store.Helpdesk, store.Messages, settings, service.GlobalHelpdeskConfig(settings),
		gateway, cfg.OwnerID, me.ID, 0, log)
	tasks.SetHelpdesk(helpdesk)
	bot := tgbot.New(api, tgbot.Config{OwnerID: cfg.OwnerID, BotID: me.ID, BotUsername: me.Username, MainWebApp: me.HasMainWebApp},
		tasks, settings, conns, helpdesk, log)
	tasks.SetObserver(bot)
	triage := service.NewTriageService(store.Messages, store.Tasks, store.Analyses, conns, settings, botsSvc, registry, bot, log)
	bot.SetTriage(triage)
	helpdesk.SetTriage(triage)
	helpdesk.SetTickets(tasks)
	backups := service.NewBackupService(store, filepath.Join(filepath.Dir(cfg.DBPath), "backups"), settings, gateway, log)
	scheduler := service.NewScheduler(tasks, settings, store.Messages, bot, helpdesk, backups, log)
	hdRegistry := service.NewBotRegistry(helpdesk)
	botMgr := newBotManager(ctx, cfg, store, settings, botsSvc, conns, tasks, triage, scheduler, hdRegistry, log)

	settings.OnChange(func(old, next domain.Settings) {
		if providerSignature(old) != providerSignature(next) {
			registerProviders(registry, next, cfg.Socks5Addr)
			log.Info("AI providers reconfigured")
		}
		if old.WebAppPublicURL != next.WebAppPublicURL {
			bot.EnsureMenuButton(ctx)
			botMgr.BroadcastMenuButton(ctx)
		}
	})
	var restartRequested atomic.Bool
	webappSrv := webapp.New(webapp.Config{
		Addr: cfg.WebAppAddr, BotToken: cfg.TelegramToken, OwnerID: cfg.OwnerID, DevInsecure: cfg.WebAppDevInsecure,
		TelegramAPIURL: cfg.TelegramAPIURL, Socks5Addr: cfg.Socks5Addr,
	}, webapp.Deps{
		Tasks: tasks, Settings: settings, Triage: triage, Helpdesk: helpdesk, Backups: backups, Bots: botsSvc,
		Helpdesks: hdRegistry, Restart: func() { restartRequested.Store(true); stop() },
	}, log)

	helpdesk.Start(ctx)
	if err := triage.Start(ctx); err != nil {
		return err
	}
	botMgr.Reconcile(botsSvc.List())
	botsSvc.OnChange(botMgr.Reconcile)

	var wg sync.WaitGroup
	wg.Go(func() { scheduler.Run(ctx) })
	wg.Go(func() {
		if err := webappSrv.Run(ctx); err != nil {
			log.Error("webapp server stopped", "err", err)
		}
	})

	st := settings.Get()
	chain := make([]string, len(st.AIChain))
	for i, k := range st.AIChain {
		chain[i] = k.Provider
	}
	log.Info("service started", "version", version, "ai_chain", chain, "timezone", settings.Location().String(),
		"webapp_addr", cfg.WebAppAddr, "webapp_dev_insecure", cfg.WebAppDevInsecure, "webapp_public_url", st.WebAppPublicURL,
		"helpdesk", st.Helpdesk.Active())
	if len(chain) == 0 {
		log.Warn("no AI API keys configured: messages are stored but not triaged until keys are added in the Mini App settings")
	}

	bot.Run(ctx) // blocks until SIGINT/SIGTERM or a restart request

	log.Info("shutting down")
	botMgr.StopAll()
	if !triage.Wait(25 * time.Second) {
		log.Warn("triage workers did not finish in time; unanalyzed messages will be recovered on next start")
	}
	if !helpdesk.Wait(10 * time.Second) {
		log.Warn("helpdesk background jobs did not finish in time")
	}
	wg.Wait()
	log.Info("stopped")
	if restartRequested.Load() {
		return errRestart
	}
	return nil
}
