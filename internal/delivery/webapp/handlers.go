package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
	"tgtriage/internal/telegram"
)

func (s *Server) loc() *time.Location { return s.settings.Location() }

// scopeFor returns the task scope a request may see: operators only work with helpdesk tickets.
func (s *Server) scopeFor(r *http.Request) domain.TaskScope {
	if !principalFrom(r).isOwner() {
		return domain.ScopeHelpdesk
	}
	return domain.ParseTaskScope(r.URL.Query().Get("scope"))
}

// botConnFor resolves the connection_id a request's task/dialog queries should be narrowed to:
// "" means every bot merged (owner only, no ?bot= given or bot=all). An operator is always forced
// to their own bot so one organization's operators can never see another's tickets.
func (s *Server) botConnFor(r *http.Request) string {
	p := principalFrom(r)
	if !p.isOwner() {
		return domain.HelpdeskConnectionFor(p.BotDBID)
	}
	q := strings.TrimSpace(r.URL.Query().Get("bot"))
	if q == "" || q == "all" {
		return ""
	}
	id, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		return ""
	}
	return domain.HelpdeskConnectionFor(id)
}

// canAccess reports whether the requester may see t: the owner sees everything, an operator only
// their own bot's helpdesk tickets.
func (s *Server) canAccess(r *http.Request, t *domain.Task) bool {
	p := principalFrom(r)
	if p.isOwner() {
		return true
	}
	return t.IsHelpdesk() && domain.ParseHelpdeskBotID(t.ConnectionID) == p.BotDBID
}

// helpdeskFor resolves which bot's HelpdeskService a helpdesk request targets: an operator's own
// bot, or the owner's explicit ?bot= choice (default 0 — the main bot, preserving the single-bot
// behavior when no additional bots are in use).
func (s *Server) helpdeskFor(r *http.Request) (*service.HelpdeskService, int64, bool) {
	p := principalFrom(r)
	botID := p.BotDBID
	if p.isOwner() {
		if q := strings.TrimSpace(r.URL.Query().Get("bot")); q != "" && q != "all" {
			if id, err := strconv.ParseInt(q, 10, 64); err == nil {
				botID = id
			}
		}
	}
	hd, ok := s.helpdeskRuntime(botID)
	return hd, botID, ok
}

// hdTopicURL links to a user's topic without depending on any one HelpdeskService instance's
// currently-active group — safe to use for a merged, cross-bot dialogs list.
func hdTopicURL(u *domain.HelpdeskUser) string {
	if u.TopicID == 0 || u.GroupID == 0 {
		return ""
	}
	return domain.TopicLink(u.GroupID, u.TopicID)
}

// loadTask loads a task the requester may access.
func (s *Server) loadTask(w http.ResponseWriter, r *http.Request) (*domain.Task, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return nil, false
	}
	t, err := s.tasks.Get(r.Context(), id)
	if err != nil {
		handleErr(w, err)
		return nil, false
	}
	if !s.canAccess(r, t) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return t, true
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	// For the owner (who may browse every bot from one session) the Dialogs tab shows up as soon
	// as any bot's desk is on; an operator's own bot decides it for them.
	hd, _, ok := s.helpdeskFor(r)
	var active, enabled bool
	if p.isOwner() {
		if s.helpdesk.Config().Active() {
			active = true
		}
		enabled = s.helpdesk.Config().Enabled
		for _, b := range s.bots.List() {
			active = active || b.Helpdesk.Active()
			enabled = enabled || b.Helpdesk.Enabled
		}
	} else if ok {
		cfg := hd.Config()
		active, enabled = cfg.Active(), cfg.Enabled
	}
	writeJSON(w, http.StatusOK, Me{UserID: p.ID, Name: p.Name, Role: p.Role, Helpdesk: active, HelpdeskEnabled: enabled})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	connID := s.botConnFor(r)
	o, err := s.tasks.Overview(r.Context(), s.scopeFor(r), connID)
	if err != nil {
		handleErr(w, err)
		return
	}
	out := Overview{New: o.New, InProgress: o.InProgress, Snoozed: o.Snoozed, Overdue: o.Overdue}
	if o.Connection != nil && principalFrom(r).isOwner() {
		out.Connection = &Connection{
			Name: o.Connection.UserName, Enabled: o.Connection.Enabled,
			CanReply: o.Connection.CanReply, CanRead: o.Connection.CanReadMessages,
		}
	}
	if hd, _, ok := s.helpdeskFor(r); ok {
		f := domain.HelpdeskUserFilter{AwaitingOnly: true, Limit: 1}
		var n int
		var err error
		if connID == "" && principalFrom(r).isOwner() {
			_, n, err = hd.UsersAcrossBots(r.Context(), f)
		} else {
			_, n, err = hd.Users(r.Context(), f)
		}
		if err == nil {
			out.Awaiting = n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	statuses, ok := statusFilters[q.Get("status")]
	if !ok && q.Get("status") != "" {
		writeError(w, http.StatusBadRequest, "unknown status filter")
		return
	}
	priorities, ok := priorityFilters[q.Get("priority")]
	if !ok && q.Get("priority") != "" {
		writeError(w, http.StatusBadRequest, "unknown priority filter")
		return
	}
	limit := queryInt(q, "limit", 20, 1, 200)
	offset := queryInt(q, "offset", 0, 0, 1<<30)

	items, total, err := s.tasks.List(r.Context(), domain.TaskFilter{
		Statuses: statuses, Priorities: priorities, Scope: s.scopeFor(r), ConnectionID: s.botConnFor(r),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TaskList{Items: toTaskDTOs(items, s.loc()), Total: total, Limit: limit, Offset: offset})
}

func (s *Server) taskResponse(ctx context.Context, t *domain.Task) Task {
	dto := toTaskDTO(*t, s.loc())
	if t.IsHelpdesk() && t.HasChat() {
		if hd, ok := s.helpdeskRuntime(domain.ParseHelpdeskBotID(t.ConnectionID)); ok {
			if u, err := hd.User(ctx, t.ChatID); err == nil {
				hu := s.hdUserDTO(u)
				dto.HelpdeskUser = &hu
			}
		}
	}
	return dto
}

func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := s.tasks.SetStatus(r.Context(), t.ID, domain.TaskStatus(body.Status))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

// handleTaskClose backs the "✅ Закрыть" flow, mirroring the bot's confirmation: SendMessage=true
// sends service.DoneMessage to the contact before closing, matching NotifyDoneOnClose semantics.
func (s *Server) handleTaskClose(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		SendMessage bool   `json:"send_message"`
		Text        string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var err error
	switch {
	case body.Text != "":
		t, err = s.tasks.CloseWithMessage(r.Context(), t.ID, body.Text)
	case body.SendMessage:
		t, err = s.tasks.CloseWithMessage(r.Context(), t.ID, service.DoneMessage)
	default:
		t, err = s.tasks.SetStatus(r.Context(), t.ID, domain.StatusDone)
	}
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

func (s *Server) handleTaskSnooze(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Until string `json:"until"` // RFC3339
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	until, err := time.Parse(time.RFC3339, body.Until)
	if err != nil {
		writeError(w, http.StatusBadRequest, "until must be an RFC3339 timestamp")
		return
	}
	t, err = s.tasks.Snooze(r.Context(), t.ID, until)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

func (s *Server) handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Importance  string `json:"importance"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := s.tasks.Edit(r.Context(), t.ID, service.EditInput{
		Title: body.Title, Description: body.Description,
		Priority: domain.Priority(body.Priority), Importance: domain.Priority(body.Importance),
	})
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

func (s *Server) handleTaskRemind(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		At string `json:"at"` // RFC3339; empty cancels the reminder
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var (
		res *domain.Task
		err error
	)
	if body.At == "" {
		res, err = s.tasks.ClearReminder(r.Context(), t.ID)
	} else {
		at, perr := time.Parse(time.RFC3339, body.At)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "at must be an RFC3339 timestamp")
			return
		}
		res, err = s.tasks.SetReminder(r.Context(), t.ID, at)
	}
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), res))
}

func (s *Server) handleTaskMerge(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		TargetID int64 `json:"target_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	target, err := s.tasks.Get(r.Context(), body.TargetID)
	if err != nil {
		handleErr(w, err)
		return
	}
	if !s.canAccess(r, target) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	res, err := s.tasks.Merge(r.Context(), t.ID, body.TargetID)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), res))
}

func (s *Server) handleTaskDraft(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	t, err := s.tasks.SendDraft(r.Context(), t.ID)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

func (s *Server) handleTaskReply(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := s.tasks.SendReply(r.Context(), t.ID, body.Text)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(r.Context(), t))
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	d, err := s.tasks.BuildDigest(r.Context(), s.scopeFor(r), s.botConnFor(r))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDigestDTO(d, s.loc()))
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r.URL.Query(), "days", 30, 1, 365)
	st, err := s.tasks.Stats(r.Context(), s.scopeFor(r), s.botConnFor(r), days)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toStatsDTO(st))
}

// ---------- helpdesk ----------

func (s *Server) hdUserDTO(u *domain.HelpdeskUser) HelpdeskUser {
	return toHDUserDTO(u, hdTopicURL(u), s.loc())
}

func (s *Server) handleHDUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(q, "limit", 30, 1, 200)
	offset := queryInt(q, "offset", 0, 0, 1<<30)
	f := domain.HelpdeskUserFilter{AwaitingOnly: q.Get("filter") == "awaiting", BannedOnly: q.Get("filter") == "banned",
		Query: q.Get("q"), Limit: limit, Offset: offset}

	var (
		users []domain.HelpdeskUser
		total int
		err   error
	)
	botParam := strings.TrimSpace(q.Get("bot"))
	merged := principalFrom(r).isOwner() && (botParam == "" || botParam == "all")
	if merged {
		users, total, err = s.helpdesk.UsersAcrossBots(r.Context(), f)
	} else {
		hd, _, ok := s.helpdeskFor(r)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
			return
		}
		users, total, err = hd.Users(r.Context(), f)
	}
	if err != nil {
		handleErr(w, err)
		return
	}
	items := make([]HelpdeskUser, len(users))
	for i := range users {
		items[i] = s.hdUserDTO(&users[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleHDUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	hd, botID, ok := s.helpdeskFor(r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
		return
	}
	u, err := hd.User(r.Context(), id)
	if err != nil {
		handleErr(w, err)
		return
	}
	msgs, err := hd.Conversation(r.Context(), id, 150)
	if err != nil {
		handleErr(w, err)
		return
	}
	tickets, _, err := s.tasks.List(r.Context(), domain.TaskFilter{ConnectionID: domain.HelpdeskConnectionFor(botID), ChatID: id, Limit: 50})
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HelpdeskDialog{
		User: s.hdUserDTO(u), Messages: toDialogMessages(msgs, s.loc()), Tickets: toTaskDTOs(tickets, s.loc()),
	})
}

func (s *Server) handleHDReply(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	hd, _, ok := s.helpdeskFor(r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
		return
	}
	if err := hd.ReplyToUser(r.Context(), id, body.Text); err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleHDTopic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Closed bool `json:"closed"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	hd, _, ok := s.helpdeskFor(r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
		return
	}
	u, err := hd.SetTopicClosed(r.Context(), id, body.Closed)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.hdUserDTO(u))
}

func (s *Server) handleHDTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	hd, _, ok := s.helpdeskFor(r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	t, err := hd.ForceTicket(service.WithActor(ctx, service.ActorFrom(r.Context())), id)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(ctx, t))
}

func (s *Server) handleHDCheck(w http.ResponseWriter, r *http.Request) {
	hd, _, ok := s.helpdeskFor(r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
		return
	}
	res, err := hd.CheckGroup(r.Context())
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- settings ----------

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsDTO())
}

func (s *Server) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		Values  map[string]json.RawMessage `json:"values"`
		AIChain *[]aiKeyPatch              `json:"ai_chain"`
	}
	if !decodeJSON(w, r, &patch) {
		return
	}
	if len(patch.Values) > 0 {
		if _, err := s.settings.UpdateFields(r.Context(), patch.Values); err != nil {
			handleErr(w, err)
			return
		}
	}
	if patch.AIChain != nil {
		if _, err := s.settings.Update(r.Context(), func(st *domain.Settings) { applyChain(st, *patch.AIChain) }); err != nil {
			handleErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.settingsDTO())
}

func (s *Server) handleSettingsReset(w http.ResponseWriter, r *http.Request) {
	if err := s.settings.Reset(r.Context()); err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsDTO())
}

func (s *Server) settingsDTO() Settings {
	st := s.settings.Get()
	fields := service.SettingFields()
	out := Settings{
		Groups:    service.SettingGroups,
		Fields:    make([]SettingValue, len(fields)),
		AIChain:   toAIChainDTO(st.AIChain),
		Providers: domain.Providers,
	}
	for i, f := range fields {
		out.Fields[i] = SettingValue{SettingField: f, Value: f.Value(&st)}
	}
	return out
}

// handleProviderTest checks every AI chain entry and reports each one separately.
func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	results, err := s.triage.Probe(ctx)
	if err != nil {
		handleErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(results))
	for _, res := range results {
		item := map[string]any{
			"index":      res.Index,
			"provider":   res.Provider,
			"model":      res.Model,
			"key_masked": res.KeyMask,
			"latency_ms": res.Latency.Milliseconds(),
		}
		if res.Err != nil {
			item["error"] = res.Err.Error()
		} else {
			item["analysis"] = res.Analysis
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// ---------- backups & system ----------

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	list, err := s.backups.List()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "dir": s.backups.Dir()})
}

func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	info, err := s.backups.Run(ctx)
	if err != nil && info.Name == "" {
		handleErr(w, err)
		return
	}
	resp := map[string]any{"backup": info}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.restart == nil {
		writeError(w, http.StatusNotImplemented, "restart is not available")
		return
	}
	s.log.Warn("restart requested from the Mini App", "user_id", principalFrom(r).ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	time.AfterFunc(500*time.Millisecond, s.restart)
}

// ---------- bots ----------

func (s *Server) handleBotList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": toBotDTOs(s.bots.List())})
}

// verifyBotToken calls getMe to make sure a pasted token is real and to fetch the bot's @username
// for display — the same check main.go does for the bot from .env.
func (s *Server) verifyBotToken(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	client := telegram.NewClient(token, s.cfg.TelegramAPIURL, s.cfg.Socks5Addr, s.log.With("component", "telegram"))
	me, err := client.GetMe(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: не удалось подключиться к боту, проверьте токен", domain.ErrInvalidInput)
	}
	return me.Username, nil
}

func (s *Server) handleBotAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Label string `json:"label"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "укажите токен бота")
		return
	}
	username, err := s.verifyBotToken(r.Context(), token)
	if err != nil {
		handleErr(w, err)
		return
	}
	b, err := s.bots.Add(r.Context(), token, username, strings.TrimSpace(body.Label))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBotDTO(*b))
}

// botHelpdeskPatch is the partial-update body of a bot's own support desk config — every field is
// optional, only the ones present are changed, mirroring handleSettingsPatch's PATCH model.
type botHelpdeskPatch struct {
	Enabled          *bool   `json:"enabled"`
	GroupID          *int64  `json:"group_id"`
	TriageEnabled    *bool   `json:"triage_enabled"`
	About            *string `json:"about"`
	GreetingEnabled  *bool   `json:"greeting_enabled"`
	GreetingText     *string `json:"greeting_text"`
	AutoReplyEnabled *bool   `json:"autoreply_enabled"`
	AutoReplyText    *string `json:"autoreply_text"`
	HoursEnabled     *bool   `json:"hours_enabled"`
	HoursStart       *string `json:"hours_start"`
	HoursEnd         *string `json:"hours_end"`
	HoursDays        *string `json:"hours_days"`
	OffHoursText     *string `json:"offhours_text"`
	ReminderMinutes  *int    `json:"reminder_minutes"`
}

func (s *Server) handleBotPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid bot id")
		return
	}
	var body struct {
		Token       *string           `json:"token"`
		Label       *string           `json:"label"`
		Active      *bool             `json:"active"`
		Sensitivity *string           `json:"sensitivity"`
		Helpdesk    *botHelpdeskPatch `json:"helpdesk"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var newUsername string
	if body.Token != nil {
		token := strings.TrimSpace(*body.Token)
		if token == "" {
			writeError(w, http.StatusBadRequest, "токен не может быть пустым")
			return
		}
		var err error
		if newUsername, err = s.verifyBotToken(r.Context(), token); err != nil {
			handleErr(w, err)
			return
		}
	}
	if body.Sensitivity != nil {
		switch domain.Sensitivity(*body.Sensitivity) {
		case "", domain.SensitivityLow, domain.SensitivityMedium, domain.SensitivityHigh:
		default:
			writeError(w, http.StatusBadRequest, "неизвестная чувствительность")
			return
		}
	}
	b, err := s.bots.Update(r.Context(), id, func(bot *domain.Bot) {
		if body.Token != nil {
			bot.Token, bot.Username = strings.TrimSpace(*body.Token), newUsername
		}
		if body.Label != nil {
			bot.Label = strings.TrimSpace(*body.Label)
		}
		if body.Active != nil {
			bot.Active = *body.Active
		}
		if body.Sensitivity != nil {
			bot.Sensitivity = domain.Sensitivity(*body.Sensitivity)
		}
		if h := body.Helpdesk; h != nil {
			hd := &bot.Helpdesk
			setB(&hd.Enabled, h.Enabled)
			setI64(&hd.GroupID, h.GroupID)
			setB(&hd.TriageEnabled, h.TriageEnabled)
			setS(&hd.About, h.About)
			setB(&hd.GreetingEnabled, h.GreetingEnabled)
			setS(&hd.GreetingText, h.GreetingText)
			setB(&hd.AutoReplyEnabled, h.AutoReplyEnabled)
			setS(&hd.AutoReplyText, h.AutoReplyText)
			setB(&hd.HoursEnabled, h.HoursEnabled)
			setS(&hd.HoursStart, h.HoursStart)
			setS(&hd.HoursEnd, h.HoursEnd)
			setS(&hd.HoursDays, h.HoursDays)
			setS(&hd.OffHoursText, h.OffHoursText)
			setI(&hd.ReminderMinutes, h.ReminderMinutes)
		}
	})
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBotDTO(b))
}

func setB(dst *bool, v *bool) {
	if v != nil {
		*dst = *v
	}
}

func setS(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

func setI(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}

func setI64(dst *int64, v *int64) {
	if v != nil {
		*dst = *v
	}
}

func (s *Server) handleBotDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid bot id")
		return
	}
	if err := s.bots.Delete(r.Context(), id); err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func queryInt(q url.Values, key string, def, minV, maxV int) int {
	v, err := strconv.Atoi(q.Get(key))
	if err != nil || v < minV || v > maxV {
		return def
	}
	return v
}

// handleHDBan bans the user as spam ({"banned": true}) or lifts the ban ({"banned": false}).
func (s *Server) handleHDBan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Banned bool `json:"banned"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	hd, _, ok := s.helpdeskFor(r)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "бот сейчас не запущен")
		return
	}
	u, err := hd.SetBanned(r.Context(), id, body.Banned)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.hdUserDTO(u))
}
