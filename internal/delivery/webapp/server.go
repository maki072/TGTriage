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
	Addr              string // listen address, e.g. ":8080"
	BotToken          string // used to validate Telegram initData
	OwnerID           int64
	Location          *time.Location
	ClaudePresets     []string
	GeminiPresets     []string
	GroqPresets       []string
	MistralPresets    []string
	OpenRouterPresets []string
	// DevInsecure allows requests without a valid Telegram initData when they originate from
	// a private/loopback address — lets you open the Mini App straight from a LAN browser
	// before a public HTTPS domain (Cloudflare Tunnel or a reverse proxy) is wired up. Every
	// such request is logged at WARN. Never enable this on an internet-reachable deployment.
	DevInsecure bool
}

// Server is the Mini App HTTP delivery layer.
type Server struct {
	cfg      Config
	tasks    *service.TaskService
	settings *service.SettingsService
	conns    *service.ConnectionService
	triage   *service.TriageService
	log      *slog.Logger
	http     *http.Server
}

func New(cfg Config, tasks *service.TaskService, settings *service.SettingsService, conns *service.ConnectionService,
	triage *service.TriageService, log *slog.Logger) *Server {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	s := &Server{cfg: cfg, tasks: tasks, settings: settings, conns: conns, triage: triage, log: log.With("component", "webapp")}
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
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
	api.HandleFunc("GET /api/overview", s.handleOverview)
	api.HandleFunc("GET /api/tasks", s.handleTaskList)
	api.HandleFunc("GET /api/tasks/{id}", s.handleTaskGet)
	api.HandleFunc("POST /api/tasks/{id}/status", s.handleTaskStatus)
	api.HandleFunc("POST /api/tasks/{id}/close", s.handleTaskClose)
	api.HandleFunc("POST /api/tasks/{id}/snooze", s.handleTaskSnooze)
	api.HandleFunc("POST /api/tasks/{id}/draft", s.handleTaskDraft)
	api.HandleFunc("POST /api/tasks/{id}/reply", s.handleTaskReply)
	api.HandleFunc("GET /api/digest", s.handleDigest)
	api.HandleFunc("GET /api/stats", s.handleStats)
	api.HandleFunc("GET /api/settings", s.handleSettingsGet)
	api.HandleFunc("POST /api/settings", s.handleSettingsPatch)
	api.HandleFunc("POST /api/settings/reset", s.handleSettingsReset)
	api.HandleFunc("POST /api/provider/test", s.handleProviderTest)
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

// auth validates the Telegram Mini App launch data and restricts access to the configured owner.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initData := extractInitData(r)
		if initData != "" {
			userID, ok := validateInitData(s.cfg.BotToken, initData)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid Telegram init data")
				return
			}
			if userID != s.cfg.OwnerID {
				writeError(w, http.StatusForbidden, "not the bot owner")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if s.cfg.DevInsecure && isPrivateAddr(r.RemoteAddr) {
			s.log.Warn("webapp request without Telegram auth allowed via DevInsecure", "remote", r.RemoteAddr, "path", r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "missing Telegram init data")
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
