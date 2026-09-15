package webapp

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"tgtriage/internal/domain"
	"tgtriage/internal/service"
)

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.tasks.Overview(r.Context())
	if err != nil {
		handleErr(w, err)
		return
	}
	out := Overview{New: o.New, InProgress: o.InProgress, Snoozed: o.Snoozed, Overdue: o.Overdue}
	if o.Connection != nil {
		out.Connection = &Connection{
			Name: o.Connection.UserName, Enabled: o.Connection.Enabled,
			CanReply: o.Connection.CanReply, CanRead: o.Connection.CanReadMessages,
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
		Statuses: statuses, Priorities: priorities, Limit: limit, Offset: offset,
	})
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TaskList{Items: toTaskDTOs(items, s.cfg.Location), Total: total, Limit: limit, Offset: offset})
}

func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	t, err := s.tasks.Get(r.Context(), id)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(*t, s.cfg.Location))
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := s.tasks.SetStatus(r.Context(), id, domain.TaskStatus(body.Status))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(*t, s.cfg.Location))
}

// handleTaskClose backs the "✅ Закрыть" flow, mirroring the bot's confirmation: SendMessage=true
// sends service.DoneMessage to the contact before closing, matching NotifyDoneOnClose semantics.
func (s *Server) handleTaskClose(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	var body struct {
		SendMessage bool `json:"send_message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var (
		t   *domain.Task
		err error
	)
	if body.SendMessage {
		t, err = s.tasks.CloseWithMessage(r.Context(), id, service.DoneMessage)
	} else {
		t, err = s.tasks.SetStatus(r.Context(), id, domain.StatusDone)
	}
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(*t, s.cfg.Location))
}

func (s *Server) handleTaskSnooze(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
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
	t, err := s.tasks.Snooze(r.Context(), id, until)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(*t, s.cfg.Location))
}

func (s *Server) handleTaskDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	t, err := s.tasks.SendDraft(r.Context(), id)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(*t, s.cfg.Location))
}

func (s *Server) handleTaskReply(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := s.tasks.SendReply(r.Context(), id, body.Text)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(*t, s.cfg.Location))
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	d, err := s.tasks.BuildDigest(r.Context())
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDigestDTO(d, s.cfg.Location))
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r.URL.Query(), "days", 30, 1, 365)
	st, err := s.tasks.Stats(r.Context(), days)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toStatsDTO(st))
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsDTO())
}

func (s *Server) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var patch settingsPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	if _, err := s.settings.Update(r.Context(), func(st *domain.Settings) { patch.apply(st) }); err != nil {
		handleErr(w, err)
		return
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
	return Settings{
		AIChain:     toAIChainDTO(st.AIChain),
		ClaudeModel: st.ClaudeModel, GeminiModel: st.GeminiModel, GroqModel: st.GroqModel,
		MistralModel: st.MistralModel, OpenRouterModel: st.OpenRouterModel,
		DebounceSeconds: st.DebounceSeconds, Sensitivity: string(st.Sensitivity),
		DigestEnabled: st.DigestEnabled, DigestTime: st.DigestTime, TriagePaused: st.TriagePaused,
		MarkReadOnWork: st.MarkReadOnWork, NotifyDoneOnClose: st.NotifyDoneOnClose,
		Providers: domain.Providers, ClaudePresets: s.cfg.ClaudePresets, GeminiPresets: s.cfg.GeminiPresets,
		GroqPresets: s.cfg.GroqPresets, MistralPresets: s.cfg.MistralPresets, OpenRouterPresets: s.cfg.OpenRouterPresets,
	}
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

func queryInt(q url.Values, key string, def, minV, maxV int) int {
	v, err := strconv.Atoi(q.Get(key))
	if err != nil || v < minV || v > maxV {
		return def
	}
	return v
}
