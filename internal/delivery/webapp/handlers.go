package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
)

func (s *Server) loc() *time.Location { return s.settings.Location() }

// scopeFor returns the task scope a request may see: operators only work with helpdesk tickets.
func (s *Server) scopeFor(r *http.Request) domain.TaskScope {
	if !principalFrom(r).isOwner() {
		return domain.ScopeHelpdesk
	}
	return domain.ParseTaskScope(r.URL.Query().Get("scope"))
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
	if !principalFrom(r).isOwner() && !t.IsHelpdesk() {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return t, true
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	st := s.settings.Get()
	writeJSON(w, http.StatusOK, Me{UserID: p.ID, Name: p.Name, Role: p.Role, Helpdesk: st.Helpdesk.Active(),
		HelpdeskEnabled: st.Helpdesk.Enabled})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.tasks.Overview(r.Context(), s.scopeFor(r))
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
	if s.helpdesk.Active() {
		if _, n, err := s.helpdesk.Users(r.Context(), domain.HelpdeskUserFilter{AwaitingOnly: true, Limit: 1}); err == nil {
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
		Statuses: statuses, Priorities: priorities, Scope: s.scopeFor(r), Limit: limit, Offset: offset,
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
		if u, err := s.helpdesk.User(ctx, t.ChatID); err == nil {
			hu := s.hdUserDTO(u)
			dto.HelpdeskUser = &hu
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
	d, err := s.tasks.BuildDigest(r.Context(), s.scopeFor(r))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDigestDTO(d, s.loc()))
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r.URL.Query(), "days", 30, 1, 365)
	st, err := s.tasks.Stats(r.Context(), s.scopeFor(r), days)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toStatsDTO(st))
}

// ---------- helpdesk ----------

func (s *Server) hdUserDTO(u *domain.HelpdeskUser) HelpdeskUser {
	return toHDUserDTO(u, s.helpdesk.TopicURL(u), s.loc())
}

func (s *Server) handleHDUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(q, "limit", 30, 1, 200)
	offset := queryInt(q, "offset", 0, 0, 1<<30)
	users, total, err := s.helpdesk.Users(r.Context(), domain.HelpdeskUserFilter{
		AwaitingOnly: q.Get("filter") == "awaiting", Query: q.Get("q"), Limit: limit, Offset: offset,
	})
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
	u, err := s.helpdesk.User(r.Context(), id)
	if err != nil {
		handleErr(w, err)
		return
	}
	msgs, err := s.helpdesk.Conversation(r.Context(), id, 150)
	if err != nil {
		handleErr(w, err)
		return
	}
	tickets, _, err := s.tasks.List(r.Context(), domain.TaskFilter{Scope: domain.ScopeHelpdesk, ChatID: id, Limit: 50})
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
	if err := s.helpdesk.ReplyToUser(r.Context(), id, body.Text); err != nil {
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
	u, err := s.helpdesk.SetTopicClosed(r.Context(), id, body.Closed)
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
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	t, err := s.helpdesk.ForceTicket(service.WithActor(ctx, service.ActorFrom(r.Context())), id)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.taskResponse(ctx, t))
}

func (s *Server) handleHDCheck(w http.ResponseWriter, r *http.Request) {
	res, err := s.helpdesk.CheckGroup(r.Context())
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

func queryInt(q url.Values, key string, def, minV, maxV int) int {
	v, err := strconv.Atoi(q.Get(key))
	if err != nil || v < minV || v > maxV {
		return def
	}
	return v
}
