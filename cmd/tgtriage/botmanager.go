package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"tgtriage/internal/config"
	tgbot "tgtriage/internal/delivery/telegram"
	"tgtriage/internal/domain"
	"tgtriage/internal/repository/sqlite"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

// botRuntime is one additional bot's live long-polling loop and everything it registered with the
// shared services, so it can be torn down again without touching any other bot.
type botRuntime struct {
	token    string
	cancel   context.CancelFunc
	done     chan struct{}
	delivery *tgbot.Bot // set once getMe succeeds; nil while starting
}

// botManager starts and stops additional bots' Telegram polling loops as BotService's list
// changes, so adding, editing or removing a bot in the Mini App takes effect immediately — no
// systemd restart, matching how every other runtime setting in this service already works.
type botManager struct {
	cfg       *config.Config
	store     *sqlite.Store
	settings  *service.SettingsService
	bots      *service.BotService
	conns     *service.ConnectionService
	tasks     *service.TaskService
	triage    *service.TriageService
	scheduler *service.Scheduler
	helpdesks *service.BotRegistry[*service.HelpdeskService] // shared with the Mini App
	log       *slog.Logger
	baseCtx   context.Context

	mu       sync.Mutex
	runtimes map[int64]*botRuntime
}

func newBotManager(baseCtx context.Context, cfg *config.Config, store *sqlite.Store, settings *service.SettingsService,
	bots *service.BotService, conns *service.ConnectionService, tasks *service.TaskService, triage *service.TriageService,
	scheduler *service.Scheduler, helpdesks *service.BotRegistry[*service.HelpdeskService], log *slog.Logger) *botManager {
	return &botManager{
		cfg: cfg, store: store, settings: settings, bots: bots, conns: conns, tasks: tasks, triage: triage,
		scheduler: scheduler, helpdesks: helpdesks, log: log.With("component", "botmanager"), baseCtx: baseCtx,
		runtimes: map[int64]*botRuntime{},
	}
}

// Reconcile starts runtimes for newly active bots, stops removed/deactivated ones, and restarts a
// bot whose token changed. Safe to call repeatedly (on startup, then on every BotService change).
func (m *botManager) Reconcile(all []domain.Bot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := make(map[int64]domain.Bot, len(all))
	for _, b := range all {
		if b.Active {
			want[b.ID] = b
		}
	}
	for id, rt := range m.runtimes {
		if _, ok := want[id]; !ok {
			m.stopLocked(id, rt)
		}
	}
	for id, b := range want {
		if rt, ok := m.runtimes[id]; ok {
			if rt.token == b.Token {
				continue
			}
			m.stopLocked(id, rt)
		}
		m.startLocked(b)
	}
}

func (m *botManager) startLocked(b domain.Bot) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	rt := &botRuntime{token: b.Token, cancel: cancel, done: make(chan struct{})}
	m.runtimes[b.ID] = rt
	go func() {
		defer close(rt.done)
		m.runOne(ctx, b, rt)
	}()
}

func (m *botManager) stopLocked(id int64, rt *botRuntime) {
	rt.cancel()
	delete(m.runtimes, id)
	log := m.log
	go func() {
		select {
		case <-rt.done:
		case <-time.After(30 * time.Second):
			log.Warn("bot runtime did not stop in time", "bot_id", id)
		}
	}()
}

// StopAll cancels every additional bot's runtime and waits (bounded) for them to finish, for a
// clean shutdown alongside the main bot.
func (m *botManager) StopAll() {
	m.mu.Lock()
	runtimes := make([]*botRuntime, 0, len(m.runtimes))
	for _, rt := range m.runtimes {
		rt.cancel()
		runtimes = append(runtimes, rt)
	}
	m.runtimes = map[int64]*botRuntime{}
	m.mu.Unlock()
	deadline := time.After(20 * time.Second)
	for _, rt := range runtimes {
		select {
		case <-rt.done:
		case <-deadline:
		}
	}
}

// BroadcastMenuButton re-points every live additional bot's menu button — called when
// WebAppPublicURL changes, mirroring the main bot's own settings.OnChange hook.
func (m *botManager) BroadcastMenuButton(ctx context.Context) {
	m.mu.Lock()
	live := make([]*tgbot.Bot, 0, len(m.runtimes))
	for _, rt := range m.runtimes {
		if rt.delivery != nil {
			live = append(live, rt.delivery)
		}
	}
	m.mu.Unlock()
	for _, b := range live {
		b.EnsureMenuButton(ctx)
	}
}

func (m *botManager) runOne(ctx context.Context, b domain.Bot, rt *botRuntime) {
	log := m.log.With("bot_id", b.ID, "label", b.Label)
	api := telegram.NewClient(b.Token, m.cfg.TelegramAPIURL, m.cfg.Socks5Addr, log.With("component", "telegram"))
	me, err := api.GetMe(ctx)
	if err != nil {
		log.Error("bot getMe failed, not starting", "err", err)
		return
	}
	log.Info("additional bot authorized", "username", me.Username, "telegram_bot_id", me.ID, "main_web_app", me.HasMainWebApp)

	gateway := tgbot.NewGateway(api, m.cfg.OwnerID, me.ID)
	hd := service.NewHelpdeskService(m.store.Helpdesk, m.store.Messages, m.settings,
		service.BotHelpdeskConfig(m.bots, b.ID), gateway, m.cfg.OwnerID, me.ID, b.ID, log)
	hd.SetTriage(m.triage)
	hd.SetTickets(m.tasks)
	delivery := tgbot.New(api, tgbot.Config{OwnerID: m.cfg.OwnerID, BotID: me.ID, BotUsername: me.Username, MainWebApp: me.HasMainWebApp, BotDBID: b.ID},
		m.tasks, m.settings, m.conns, hd, log)
	delivery.SetTriage(m.triage)
	hd.Start(ctx)

	m.mu.Lock()
	rt.delivery = delivery
	m.mu.Unlock()

	m.triage.RegisterNotifier(b.ID, delivery)
	m.tasks.RegisterBot(b.ID, hd, delivery)
	m.scheduler.RegisterBot(b.ID, delivery, hd)
	m.helpdesks.Set(b.ID, hd)
	defer func() {
		m.triage.UnregisterNotifier(b.ID)
		m.tasks.UnregisterBot(b.ID)
		m.scheduler.UnregisterBot(b.ID)
		m.helpdesks.Unset(b.ID)
	}()

	delivery.Run(ctx) // blocks until ctx is cancelled (bot deactivated/removed, or shutdown)
	if !hd.Wait(10 * time.Second) {
		log.Warn("helpdesk background jobs did not finish in time")
	}
}
