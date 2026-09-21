package webapp

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"tgtriage/internal/service"
)

//go:embed static
var staticFS embed.FS

// Config configures the Mini App HTTP server.
type Config struct {
	Addr     string // listen address, e.g. ":8080"
	BotToken string // used to validate Telegram initData of the main bot
	OwnerID  int64
	// TelegramAPIURL and Socks5Addr let the server verify a newly pasted bot token (getMe) when
	// the owner adds an additional bot from the "Боты" settings section.
	TelegramAPIURL string
	Socks5Addr     string
	// DevInsecure allows requests without a valid Telegram initData when they originate from
	// a private/loopback address — lets you open the Mini App straight from a LAN browser
	// before a public HTTPS domain is wired up. Every such request is logged at WARN. Never
	// enable this on an internet-reachable deployment.
	DevInsecure bool
}

// Deps are the use cases the Mini App works with.
type Deps struct {
	Tasks    *service.TaskService
	Settings *service.SettingsService
	Triage   *service.TriageService
	Helpdesk *service.HelpdeskService // the main bot's own desk
	Backups  *service.BackupService
	Bots     *service.BotService
	// Helpdesks resolves an additional bot's own HelpdeskService while its runtime is live (see
	// cmd/tgtriage's botManager) — nil/absent entries mean that bot isn't currently running.
	Helpdesks *service.BotRegistry[*service.HelpdeskService]
	Restart   func() // stops the service gracefully; systemd starts it again
}

// Server is the Mini App HTTP delivery layer.
type Server struct {
	cfg       Config
	tasks     *service.TaskService
	settings  *service.SettingsService
	triage    *service.TriageService
	helpdesk  *service.HelpdeskService
	backups   *service.BackupService
	bots      *service.BotService
	helpdesks *service.BotRegistry[*service.HelpdeskService]
	restart   func()
	log       *slog.Logger
	http      *http.Server
}

func New(cfg Config, deps Deps, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, tasks: deps.Tasks, settings: deps.Settings, triage: deps.Triage, helpdesk: deps.Helpdesk,
		backups: deps.Backups, bots: deps.Bots, helpdesks: deps.Helpdesks, restart: deps.Restart,
		log: log.With("component", "webapp")}
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      4 * time.Minute, // AI key probe and backups take a while
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// Run starts the server; blocks until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.DevInsecure {
		s.log.Warn("webapp DevInsecure is ON — requests from private/loopback IPs bypass Telegram auth; disable once exposed publicly")
	}
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("webapp listening", "addr", s.cfg.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	api := http.NewServeMux()
	api.HandleFunc("GET /api/me", s.handleMe)
	api.HandleFunc("GET /api/overview", s.handleOverview)
	api.HandleFunc("GET /api/tasks", s.handleTaskList)
	api.HandleFunc("GET /api/tasks/{id}", s.handleTaskGet)
	api.HandleFunc("POST /api/tasks/{id}/status", s.handleTaskStatus)
	api.HandleFunc("POST /api/tasks/{id}/close", s.handleTaskClose)
	api.HandleFunc("POST /api/tasks/{id}/snooze", s.handleTaskSnooze)
	api.HandleFunc("POST /api/tasks/{id}/draft", s.handleTaskDraft)
	api.HandleFunc("POST /api/tasks/{id}/reply", s.handleTaskReply)
	api.HandleFunc("POST /api/tasks/{id}/edit", s.handleTaskEdit)
	api.HandleFunc("POST /api/tasks/{id}/remind", s.handleTaskRemind)
	api.HandleFunc("POST /api/tasks/{id}/merge", s.handleTaskMerge)

	api.HandleFunc("GET /api/helpdesk/users", s.handleHDUsers)
	api.HandleFunc("GET /api/helpdesk/users/{id}", s.handleHDUser)
	api.HandleFunc("POST /api/helpdesk/users/{id}/reply", s.handleHDReply)
	api.HandleFunc("POST /api/helpdesk/users/{id}/topic", s.handleHDTopic)
	api.HandleFunc("POST /api/helpdesk/users/{id}/ticket", s.handleHDTicket)
	api.HandleFunc("POST /api/helpdesk/users/{id}/ban", s.handleHDBan)

	api.Handle("GET /api/digest", s.ownerOnly(s.handleDigest))
	api.Handle("GET /api/stats", s.ownerOnly(s.handleStats))
	api.Handle("GET /api/settings", s.ownerOnly(s.handleSettingsGet))
	api.Handle("POST /api/settings", s.ownerOnly(s.handleSettingsPatch))
	api.Handle("POST /api/settings/reset", s.ownerOnly(s.handleSettingsReset))
	api.Handle("POST /api/provider/test", s.ownerOnly(s.handleProviderTest))
	api.Handle("POST /api/helpdesk/check", s.ownerOnly(s.handleHDCheck))
	api.Handle("GET /api/backups", s.ownerOnly(s.handleBackupList))
	api.Handle("POST /api/backups", s.ownerOnly(s.handleBackupRun))
	api.Handle("POST /api/system/restart", s.ownerOnly(s.handleRestart))

	api.Handle("GET /api/bots", s.ownerOnly(s.handleBotList))
	api.Handle("POST /api/bots", s.ownerOnly(s.handleBotAdd))
	api.Handle("POST /api/bots/{id}", s.ownerOnly(s.handleBotPatch))
	api.Handle("DELETE /api/bots/{id}", s.ownerOnly(s.handleBotDelete))
	mux.Handle("/api/", s.recover(s.auth(api)))

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Only fails if the embed directive itself is broken — a build-time guarantee, not a runtime condition.
		panic("webapp: static assets not embedded: " + err.Error())
	}
	mux.Handle("/", s.recover(noStaticCache(http.FileServerFS(sub))))

	return mux
}

// noStaticCache disables browser caching for the embedded frontend. The app is small, redeploys
// happen often during development, and go:embed strips file mtimes (so conditional GETs wouldn't
// revalidate correctly anyway) — a stale cached app.js silently keeps running old code otherwise.
func noStaticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// recover turns a panicking handler into a 500 instead of crashing the whole service.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in webapp handler", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const (
	roleOwner    = "owner"
	roleOperator = "operator"
)

type principal struct {
	ID      int64
	Name    string
	Role    string
	BotDBID int64 // 0 = the main bot; which bot's token validated this launch (see auth)
}

type principalKey struct{}

func principalFrom(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

func (p principal) isOwner() bool { return p.Role == roleOwner }

// auth validates the Telegram Mini App launch data. It first tries the main bot's fixed token
// (the common case, and unchanged from before multi-bot); on mismatch it tries every additional
// bot's own token, since the Mini App's launch URL doesn't otherwise say which bot opened it — a
// valid signature against a specific bot's token is exactly what tells us. The owner gets full
// access on any bot; everyone else needs to be a member of that same bot's helpdesk group.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serve := func(p principal) {
			ctx := context.WithValue(r.Context(), principalKey{}, p)
			ctx = service.WithActor(ctx, service.Actor{ID: p.ID, Name: p.Name})
			next.ServeHTTP(w, r.WithContext(ctx))
		}
		initData := extractInitData(r)
		if initData != "" {
			botID, userID, ok := s.resolveInitData(initData)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid Telegram init data")
				return
			}
			p := principal{ID: userID, Name: initDataUserName(initData), Role: roleOwner, BotDBID: botID}
			if userID != s.cfg.OwnerID {
				hd, live := s.helpdeskRuntime(botID)
				if !live || !hd.IsOperator(r.Context(), userID) {
					writeError(w, http.StatusForbidden, "нет доступа: вы не владелец и не оператор хелпдеска")
					return
				}
				p.Role = roleOperator
			}
			serve(p)
			return
		}
		if s.cfg.DevInsecure && isPrivateAddr(r.RemoteAddr) {
			s.log.Warn("webapp request without Telegram auth allowed via DevInsecure", "remote", r.RemoteAddr, "path", r.URL.Path)
			serve(principal{ID: s.cfg.OwnerID, Name: "dev", Role: roleOwner})
			return
		}
		writeError(w, http.StatusUnauthorized, "missing Telegram init data")
	})
}

// resolveInitData finds which bot's token validates initData: the main bot first (the common,
// cheap case), then every active additional bot.
func (s *Server) resolveInitData(initData string) (botID, userID int64, ok bool) {
	if userID, ok := validateInitData(s.cfg.BotToken, initData); ok {
		return 0, userID, true
	}
	if s.bots == nil {
		return 0, 0, false
	}
	for _, b := range s.bots.Active() {
		if userID, ok := validateInitData(b.Token, initData); ok {
			return b.ID, userID, true
		}
	}
	return 0, 0, false
}

// helpdeskRuntime resolves botID's own HelpdeskService: the main one for 0, or the additional
// bot's instance while its polling loop is live (see botManager).
func (s *Server) helpdeskRuntime(botID int64) (*service.HelpdeskService, bool) {
	if botID == 0 {
		return s.helpdesk, s.helpdesk != nil
	}
	if s.helpdesks == nil {
		return nil, false
	}
	return s.helpdesks.For(botID)
}

func (s *Server) ownerOnly(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !principalFrom(r).isOwner() {
			writeError(w, http.StatusForbidden, "доступно только владельцу")
			return
		}
		h(w, r)
	})
}

// extractInitData reads Telegram's launch payload from the recommended "Authorization: tma <data>"
// header, falling back to a plain X-Telegram-Init-Data header for simpler clients.
func extractInitData(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "tma ") {
		return strings.TrimPrefix(auth, "tma ")
	}
	return r.Header.Get("X-Telegram-Init-Data")
}

func isPrivateAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
