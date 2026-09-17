package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"tgtriage/internal/domain"
)

// BotService keeps the list of additional (client-organization) bots in memory, persisted in the DB.
// It mirrors SettingsService's shape: an in-memory snapshot refreshed on every change, with
// listeners so the bot runtime (internal/service.BotManager) can reconcile without a restart.
type BotService struct {
	repo domain.BotRepository
	log  *slog.Logger

	mu        sync.RWMutex
	byID      map[int64]domain.Bot
	listeners []func([]domain.Bot)
}

func NewBotService(ctx context.Context, repo domain.BotRepository, log *slog.Logger) (*BotService, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &BotService{repo: repo, log: log.With("component", "bots"), byID: map[int64]domain.Bot{}}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *BotService) reload(ctx context.Context) error {
	list, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	byID := make(map[int64]domain.Bot, len(list))
	for _, b := range list {
		byID[b.ID] = b
	}
	s.mu.Lock()
	s.byID = byID
	listeners := slices.Clone(s.listeners)
	s.mu.Unlock()
	s.notify(listeners, list)
	return nil
}

func (s *BotService) notify(listeners []func([]domain.Bot), list []domain.Bot) {
	for _, fn := range listeners {
		fn(list)
	}
}

// List returns every additional bot (owner's Mini App view).
func (s *BotService) List() []domain.Bot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Bot, 0, len(s.byID))
	for _, b := range s.byID {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b domain.Bot) int { return int(a.ID - b.ID) })
	return out
}

// Active returns only the enabled bots — what the runtime should have a live polling loop for.
func (s *BotService) Active() []domain.Bot {
	all := s.List()
	out := all[:0:0]
	for _, b := range all {
		if b.Active {
			out = append(out, b)
		}
	}
	return out
}

// Get returns one bot by id.
func (s *BotService) Get(id int64) (domain.Bot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.byID[id]
	return b, ok
}

// OnChange registers a listener called with the fresh bot list after every successful change.
func (s *BotService) OnChange(fn func([]domain.Bot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// Add registers a new bot. username is the bot's own @handle (from getMe), verified by the caller
// before calling Add — the service layer stays decoupled from the Telegram client.
func (s *BotService) Add(ctx context.Context, token, username, label string) (*domain.Bot, error) {
	token = strings.TrimSpace(token)
	label = strings.TrimSpace(label)
	if token == "" {
		return nil, fmt.Errorf("%w: пустой токен", domain.ErrInvalidInput)
	}
	if label == "" {
		label = "@" + username
	}
	b := &domain.Bot{Token: token, Username: username, Label: label, Active: true, Helpdesk: domain.DefaultHelpdeskSettings()}
	if err := s.repo.Save(ctx, b); err != nil {
		return nil, err
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

// Update applies fn to the bot and persists the result.
func (s *BotService) Update(ctx context.Context, id int64, fn func(*domain.Bot)) (domain.Bot, error) {
	b, ok := s.Get(id)
	if !ok {
		return domain.Bot{}, domain.ErrNotFound
	}
	fn(&b)
	if err := s.repo.Save(ctx, &b); err != nil {
		return domain.Bot{}, err
	}
	if err := s.reload(ctx); err != nil {
		return domain.Bot{}, err
	}
	return b, nil
}

// UpdateHelpdesk applies fn to the bot's own helpdesk config — used both by the Mini App and by the
// chat "use this group for the helpdesk" offer (mirrors SettingsService.Update for the main bot).
func (s *BotService) UpdateHelpdesk(ctx context.Context, id int64, fn func(*domain.HelpdeskSettings)) error {
	_, err := s.Update(ctx, id, func(b *domain.Bot) { fn(&b.Helpdesk) })
	return err
}

// Delete removes a bot. Its historical tasks/messages/tickets are kept (tagged with its old id).
func (s *BotService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return s.reload(ctx)
}

// HelpdeskConfigStore is the small interface HelpdeskService and tgbot.Bot use to read/write
// helpdesk config without caring whether it lives in the shared Settings (main bot) or in a Bot row.
type HelpdeskConfigStore interface {
	Get() domain.HelpdeskSettings
	Update(ctx context.Context, fn func(*domain.HelpdeskSettings)) error
}

// globalHelpdeskConfig adapts the main bot's SettingsService.Helpdesk field — behavior unchanged.
type globalHelpdeskConfig struct{ settings *SettingsService }

func GlobalHelpdeskConfig(settings *SettingsService) HelpdeskConfigStore {
	return globalHelpdeskConfig{settings: settings}
}

func (c globalHelpdeskConfig) Get() domain.HelpdeskSettings { return c.settings.Get().Helpdesk }

func (c globalHelpdeskConfig) Update(ctx context.Context, fn func(*domain.HelpdeskSettings)) error {
	_, err := c.settings.Update(ctx, func(st *domain.Settings) { fn(&st.Helpdesk) })
	return err
}

// botHelpdeskConfig adapts one additional bot's own Helpdesk field.
type botHelpdeskConfig struct {
	bots *BotService
	id   int64
}

func BotHelpdeskConfig(bots *BotService, id int64) HelpdeskConfigStore {
	return botHelpdeskConfig{bots: bots, id: id}
}

func (c botHelpdeskConfig) Get() domain.HelpdeskSettings {
	b, _ := c.bots.Get(c.id)
	return b.Helpdesk
}

func (c botHelpdeskConfig) Update(ctx context.Context, fn func(*domain.HelpdeskSettings)) error {
	return c.bots.UpdateHelpdesk(ctx, c.id, fn)
}
