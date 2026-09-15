package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
)

// Notifier delivers triage results to the owner.
type Notifier interface {
	TaskCreated(ctx context.Context, t *domain.Task)
	TaskUpdated(ctx context.Context, t *domain.Task)
	AnalysisFailed(ctx context.Context, rec *domain.AnalysisRecord, contactName string)
	ForwardFailed(ctx context.Context, rec *domain.AnalysisRecord)
}

// TriageConfig tunes the triage pipeline.
type TriageConfig struct {
	MaxWait         time.Duration // hard cap of debounce window for continuous streams of messages
	ContextMessages int           // history messages sent to the LLM as context
	Workers         int           // concurrent LLM calls
	MaxRetries      int           // retries of a failed LLM call
	CallTimeout     time.Duration // timeout of one LLM call
	OwnerAbout      string        // free-form owner description for the prompt
	Location        *time.Location
	// NoisePrefilter skips the LLM call entirely for a batch that is unmistakably noise (a bare
	// "спасибо"/"ок"/emoji reaction — see noisefilter.go), saving tokens and quota on the highest-
	// volume, lowest-value traffic. Deliberately narrow: any doubt sends the batch to the model as
	// before. Default on; set false to send every batch through the LLM unconditionally.
	NoisePrefilter bool
}

// TriageService aggregates incoming messages (debounce), analyzes them with the LLM chain
// and turns actionable requests into tasks.
type TriageService struct {
	cfg      TriageConfig
	messages domain.MessageRepository
	tasks    domain.TaskRepository
	analyses domain.AnalysisRepository
	conns    *ConnectionService
	settings *SettingsService
	registry *ai.Registry
	notifier Notifier
	log      *slog.Logger

	mu        sync.Mutex
	buffers   map[chatKey]*buffer
	chatLocks map[chatKey]*sync.Mutex
	gen       uint64
	fwd       []domain.Message // forwarded messages waiting for forwardDebounce
	fwdTimer  *time.Timer
	fwdGen    uint64

	queue   chan batch
	wg      sync.WaitGroup
	baseCtx context.Context
}

type chatKey struct {
	connID string
	chatID int64
}

type buffer struct {
	ids   []int64
	first time.Time
	timer *time.Timer
	gen   uint64
}

type batch struct {
	key chatKey
	ids []int64
}

func NewTriageService(cfg TriageConfig, messages domain.MessageRepository, tasks domain.TaskRepository,
	analyses domain.AnalysisRepository, conns *ConnectionService, settings *SettingsService,
	registry *ai.Registry, notifier Notifier, log *slog.Logger) *TriageService {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 90 * time.Second
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	return &TriageService{
		cfg: cfg, messages: messages, tasks: tasks, analyses: analyses, conns: conns,
		settings: settings, registry: registry, notifier: notifier, log: log.With("component", "triage"),
		buffers:   map[chatKey]*buffer{},
		chatLocks: map[chatKey]*sync.Mutex{},
		queue:     make(chan batch, 256),
		baseCtx:   context.Background(),
	}
}

// Start launches workers and re-queues messages that were not analyzed before a restart.
func (s *TriageService) Start(ctx context.Context) error {
	s.baseCtx = ctx
	for range s.cfg.Workers {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	pending, err := s.messages.Pending(ctx)
	if err != nil {
		return fmt.Errorf("load pending messages: %w", err)
	}
	groups := map[chatKey][]int64{}
	var order []chatKey
	for _, m := range pending {
		k := chatKey{m.ConnectionID, m.ChatID}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], m.ID)
	}
	if len(order) > 0 {
		s.log.Info("recovering unanalyzed messages", "chats", len(order), "messages", len(pending))
	}
	go func() {
		for _, k := range order {
			s.enqueue(batch{key: k, ids: groups[k]})
		}
	}()
	return nil
}

// Wait blocks until workers finish in-flight analyses or timeout expires.
func (s *TriageService) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// OnIncoming stores a message from a contact and (re)starts the debounce window of its chat.
func (s *TriageService) OnIncoming(ctx context.Context, m *domain.Message) error {
	st := s.settings.Get()
	m.Outgoing = false
	m.Analyzed = st.TriagePaused
	inserted, err := s.messages.Save(ctx, m)
	if err != nil {
		return err
	}
	if !inserted || st.TriagePaused {
		return nil
	}
	s.add(chatKey{m.ConnectionID, m.ChatID}, m.ID, time.Duration(st.DebounceSeconds)*time.Second)
	return nil
}

// OnOutgoing stores a message written by the owner (context for future analyses).
func (s *TriageService) OnOutgoing(ctx context.Context, m *domain.Message) error {
	m.Outgoing = true
	m.Analyzed = true
	_, err := s.messages.Save(ctx, m)
	return err
}

// OnEdited updates the stored text; a still-buffered message is analyzed with the new text.
func (s *TriageService) OnEdited(ctx context.Context, connID string, chatID int64, messageID int, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return s.messages.UpdateText(ctx, connID, chatID, messageID, text)
}

// OnDeleted marks messages deleted so they are excluded from analysis.
func (s *TriageService) OnDeleted(ctx context.Context, connID string, chatID int64, messageIDs []int) error {
	return s.messages.MarkDeleted(ctx, connID, chatID, messageIDs)
}

// Retry re-queues the messages of a failed analysis.
func (s *TriageService) Retry(ctx context.Context, analysisID int64) error {
	rec, err := s.analyses.Get(ctx, analysisID)
	if err != nil {
		return err
	}
	if len(rec.MessageRowIDs) == 0 {
		return domain.ErrNotFound
	}
	go s.enqueue(batch{key: chatKey{rec.ConnectionID, rec.ChatID}, ids: rec.MessageRowIDs})
	return nil
}

func (s *TriageService) add(key chatKey, id int64, debounce time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	buf, ok := s.buffers[key]
	if !ok {
		buf = &buffer{first: now}
		s.buffers[key] = buf
	}
	buf.ids = append(buf.ids, id)
	wait := debounce
	if s.cfg.MaxWait > 0 {
		if remaining := s.cfg.MaxWait - now.Sub(buf.first); remaining < wait {
			wait = max(remaining, 0)
		}
	}
	if buf.timer != nil {
		buf.timer.Stop()
	}
	s.gen++
	gen := s.gen
	buf.gen = gen
	buf.timer = time.AfterFunc(wait, func() { s.flush(key, gen) })
}

func (s *TriageService) flush(key chatKey, gen uint64) {
	s.mu.Lock()
	buf, ok := s.buffers[key]
	if !ok || buf.gen != gen {
		s.mu.Unlock()
		return
	}
	delete(s.buffers, key)
	s.mu.Unlock()
	s.enqueue(batch{key: key, ids: buf.ids})
}

func (s *TriageService) enqueue(b batch) {
	select {
	case s.queue <- b:
	case <-s.baseCtx.Done():
		// messages stay unanalyzed in the DB and are recovered on next start
	}
}

func (s *TriageService) chatLock(key chatKey) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.chatLocks[key]
	if !ok {
		l = &sync.Mutex{}
		s.chatLocks[key] = l
	}
	return l
}

func (s *TriageService) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-s.queue:
			s.process(b)
		}
	}
}

func (s *TriageService) process(b batch) {
	lock := s.chatLock(b.key)
	lock.Lock()
	defer lock.Unlock()
	log := s.log.With("chat_id", b.key.chatID, "batch", len(b.ids))
	s.runDetached(log, func(ctx context.Context) error { return s.analyze(ctx, b, log) })
}

// runDetached runs one analysis bounded by the retry budget of the whole AI chain. The context is
// detached from shutdown: an in-flight analysis is allowed to finish gracefully. Panics are logged,
// not propagated.
func (s *TriageService) runDetached(log *slog.Logger, fn func(context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in triage", "panic", r)
		}
	}()
	entries := max(1, len(s.settings.Get().AIChain))
	perEntry := s.cfg.CallTimeout*time.Duration(s.cfg.MaxRetries+1) + 30*time.Second // + backoff pauses
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), perEntry*time.Duration(entries)+time.Minute)
	defer cancel()
	if err := fn(ctx); err != nil {
		log.Error("triage failed", "err", err)
	}
}

func (s *TriageService) analyze(ctx context.Context, b batch, log *slog.Logger) error {
	msgs, err := s.messages.GetByIDs(ctx, b.ids)
	if err != nil {
		return err
	}
	var live []domain.Message
	for _, m := range msgs {
		if !m.Deleted && !m.Outgoing {
			live = append(live, m)
		}
	}
	if len(live) == 0 {
		return s.messages.MarkAnalyzed(ctx, b.ids, 0)
	}
	if s.cfg.NoisePrefilter && isHeuristicNoise(live) {
		return s.recordHeuristicNoise(ctx, b, live)
	}

	st := s.settings.Get()
	if len(st.AIChain) == 0 {
		return domain.ErrProviderUnset // messages stay unanalyzed and are picked up again on restart
	}

	var ownerName string
	if conn, err := s.conns.Get(ctx, b.key.connID); err == nil {
		ownerName = conn.UserName
	}
	history, err := s.messages.History(ctx, b.key.connID, b.key.chatID, live[0].ID, s.cfg.ContextMessages)
	if err != nil {
		return err
	}
	openTasks, _, err := s.tasks.List(ctx, domain.TaskFilter{
		Statuses: []domain.TaskStatus{domain.StatusNew, domain.StatusInProgress, domain.StatusSnoozed},
		ChatID:   b.key.chatID,
		Limit:    10,
	})
	if err != nil {
		return err
	}

	first := live[0]
	in := ai.TriageInput{
		OwnerName:       ownerName,
		OwnerAbout:      s.cfg.OwnerAbout,
		Now:             time.Now(),
		Location:        s.cfg.Location,
		Sensitivity:     st.Sensitivity,
		ContactName:     first.SenderName,
		ContactUsername: first.SenderUsername,
		Context:         history,
		New:             live,
		OpenTasks:       openTasks,
	}
	req := ai.Request{System: ai.SystemPrompt(in), User: ai.UserPrompt(in), Schema: ai.AnalysisSchema()}

	rec := &domain.AnalysisRecord{
		ConnectionID:  b.key.connID,
		ChatID:        b.key.chatID,
		MessageRowIDs: b.ids,
		InputText:     batchText(live),
	}
	started := time.Now()
	res, err := s.completeChain(ctx, st, req, log)
	rec.LatencyMs = time.Since(started).Milliseconds()
	rec.Provider, rec.Model = res.Provider, res.Model
	if res.Resp != nil {
		rec.RawResponse = res.Resp.Text
		if res.Resp.Model != "" {
			rec.Model = res.Resp.Model
		}
	}
	if err != nil {
		rec.Status = domain.AnalysisError
		rec.Error = err.Error()
		if cerr := s.analyses.Create(ctx, rec); cerr != nil {
			return errors.Join(err, cerr)
		}
		if merr := s.messages.MarkAnalyzed(ctx, b.ids, rec.ID); merr != nil {
			return errors.Join(err, merr)
		}
		s.notifier.AnalysisFailed(ctx, rec, first.SenderName)
		return err
	}
	analysis := res.Analysis

	rec.Status = domain.AnalysisOK
	rec.IsTask = analysis.IsTask
	rec.Confidence = analysis.Confidence
	rec.MessageType = analysis.MessageType
	if err := s.analyses.Create(ctx, rec); err != nil {
		return err
	}
	if err := s.messages.MarkAnalyzed(ctx, b.ids, rec.ID); err != nil {
		return err
	}

	threshold := st.Sensitivity.Threshold()
	log.Info("triage result", "provider", rec.Provider, "model", rec.Model, "type", analysis.MessageType,
		"is_task", analysis.IsTask, "confidence", analysis.Confidence, "threshold", threshold,
		"update_task_id", analysis.UpdateTaskID, "latency_ms", rec.LatencyMs)
	if !analysis.IsTask || analysis.Confidence < threshold {
		return nil
	}

	if analysis.UpdateTaskID > 0 {
		t, err := s.tasks.Get(ctx, analysis.UpdateTaskID)
		if err == nil && t.ChatID == b.key.chatID && t.Status.IsOpen() {
			s.merge(t, analysis, live)
			if err := s.tasks.Update(ctx, t); err != nil {
				return err
			}
			if err := s.analyses.SetTaskID(ctx, rec.ID, t.ID); err != nil {
				log.Warn("link analysis to task", "err", err)
			}
			s.notifier.TaskUpdated(ctx, t)
			return nil
		}
		log.Warn("model referenced unknown or foreign task, creating a new one", "task_id", analysis.UpdateTaskID)
	}

	t := &domain.Task{
		ConnectionID:     b.key.connID,
		ChatID:           b.key.chatID,
		SenderID:         first.SenderID,
		SenderName:       first.SenderName,
		SenderUsername:   first.SenderUsername,
		SourceMessageIDs: telegramIDs(live),
		SourceText:       batchText(live),
		Title:            analysis.Title,
		Description:      analysis.Description,
		Priority:         domain.ParsePriority(analysis.Priority),
		Category:         domain.ParseCategory(analysis.Category),
		Deadline:         ai.ParseDeadline(analysis.Deadline, s.cfg.Location),
		DraftReply:       analysis.DraftReply,
		ReplyStrategy:    analysis.ReplyStrategy,
		Confidence:       analysis.Confidence,
		Status:           domain.StatusNew,
		AnalysisID:       rec.ID,
		Provider:         rec.Provider,
		Model:            rec.Model,
	}
	if err := s.tasks.Create(ctx, t); err != nil {
		return err
	}
	if err := s.analyses.SetTaskID(ctx, rec.ID, t.ID); err != nil {
		log.Warn("link analysis to task", "err", err)
	}
	s.notifier.TaskCreated(ctx, t)
	return nil
}

// recordHeuristicNoise handles a batch that isHeuristicNoise matched — no LLM call, no tokens
// spent. It still writes an AnalysisRecord (Provider "heuristic") so the batch shows up in
// history and stats exactly like a model-classified noise result, just distinguishable by provider.
func (s *TriageService) recordHeuristicNoise(ctx context.Context, b batch, live []domain.Message) error {
	rec := &domain.AnalysisRecord{
		ConnectionID:  b.key.connID,
		ChatID:        b.key.chatID,
		MessageRowIDs: b.ids,
		InputText:     batchText(live),
		Provider:      "heuristic",
		Status:        domain.AnalysisOK,
		IsTask:        false,
		Confidence:    1,
		MessageType:   "noise",
	}
	if err := s.analyses.Create(ctx, rec); err != nil {
		return err
	}
	if err := s.messages.MarkAnalyzed(ctx, b.ids, rec.ID); err != nil {
		return err
	}
	s.log.Debug("heuristic noise filter skipped LLM call", "chat_id", b.key.chatID, "batch", len(live))
	return nil
}

// chainResult is the outcome of running a request down the AI chain.
type chainResult struct {
	Provider string // entry that answered, or the last one tried
	Model    string
	Resp     *ai.Response // raw response of that entry, if any (set on invalid JSON too)
	Analysis *domain.Analysis
}

// completeChain sends req to the AI chain entries in order and returns the first valid analysis.
// Each entry gets its own key and its provider's model. Errors of all tried entries are reported
// together, so a failure notice shows why every key was skipped.
func (s *TriageService) completeChain(ctx context.Context, st domain.Settings, req ai.Request, log *slog.Logger) (chainResult, error) {
	var (
		res     chainResult
		errs    []error
		lastErr error
	)
	if len(st.AIChain) == 0 {
		return res, domain.ErrProviderUnset
	}
	for i, entry := range st.AIChain {
		if i > 0 && ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		res.Provider, res.Model, res.Resp = entry.Provider, st.ModelFor(entry.Provider), nil
		p, ok := s.registry.Get(entry.Provider)
		if !ok {
			lastErr = domain.ErrProviderUnset
			errs = append(errs, fmt.Errorf("#%d %s: %w", i+1, entry.Provider, lastErr))
			continue
		}
		req.APIKey, req.Model = entry.Key, res.Model
		resp, analysis, err := s.complete(ctx, p, req, i < len(st.AIChain)-1, log)
		res.Resp = resp
		if err == nil {
			if i > 0 {
				log.Info("AI chain fallback answered", "entry", i+1, "provider", entry.Provider)
			}
			res.Analysis = analysis
			return res, nil
		}
		lastErr = err
		errs = append(errs, fmt.Errorf("#%d %s: %w", i+1, entry.Provider, err))
		log.Warn("AI chain entry failed", "entry", i+1, "of", len(st.AIChain), "provider", entry.Provider,
			"key", domain.MaskKey(entry.Key), "err", err)
	}
	if len(st.AIChain) == 1 && len(errs) == 1 {
		return res, lastErr // a single key reads better without the chain numbering
	}
	return res, errors.Join(errs...)
}

// complete calls the provider with exponential backoff; invalid JSON is retried as well. With
// hasFallback, an HTTP error (quota, auth, region block, 5xx) ends the attempts right away: the
// next chain entry is a better bet than waiting on this one. Network errors are still retried —
// they usually hit every entry alike (proxy hiccup).
func (s *TriageService) complete(ctx context.Context, p ai.Provider, req ai.Request, hasFallback bool, log *slog.Logger) (*ai.Response, *domain.Analysis, error) {
	var (
		lastErr  error
		lastResp *ai.Response
	)
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<(attempt-1)) * 2 * time.Second
			select {
			case <-ctx.Done():
				return lastResp, nil, errors.Join(lastErr, ctx.Err())
			case <-time.After(delay):
			}
		}
		callCtx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
		resp, err := p.Complete(callCtx, req)
		cancel()
		if err != nil {
			lastErr = err
			var pe *ai.Error
			if !ai.IsRetryable(err) || hasFallback && errors.As(err, &pe) && pe.Status > 0 {
				break
			}
			log.Warn("LLM call failed, retrying", "attempt", attempt+1, "err", err)
			continue
		}
		lastResp = resp
		analysis, err := ai.ParseAnalysis(resp.Text)
		if err != nil {
			lastErr = fmt.Errorf("invalid model output: %w", err)
			log.Warn("LLM returned invalid JSON, retrying", "attempt", attempt+1, "err", err)
			continue
		}
		return resp, analysis, nil
	}
	return lastResp, nil, lastErr
}

func (s *TriageService) merge(t *domain.Task, a *domain.Analysis, live []domain.Message) {
	addition := a.Description
	if addition == "" {
		addition = a.Title
	}
	if addition != "" {
		stamp := time.Now().In(s.cfg.Location).Format("02.01 15:04")
		t.Description = strings.TrimSpace(t.Description + "\n\n➕ " + stamp + ": " + addition)
	}
	if p := domain.ParsePriority(a.Priority); p.Rank() < t.Priority.Rank() {
		t.Priority = p
	}
	if d := ai.ParseDeadline(a.Deadline, s.cfg.Location); d != nil {
		t.Deadline = d
	}
	if a.DraftReply != "" {
		t.DraftReply = a.DraftReply
		t.ReplyStrategy = a.ReplyStrategy
	}
	t.SourceText = strings.TrimSpace(t.SourceText + "\n" + batchText(live))
	t.SourceMessageIDs = append(t.SourceMessageIDs, telegramIDs(live)...)
}

// ProbeResult is the outcome of checking one AI chain entry.
type ProbeResult struct {
	Index    int // 1-based position in the chain
	Provider string
	Model    string
	KeyMask  string
	Latency  time.Duration
	Analysis *domain.Analysis // set when Err is nil
	Err      error
}

// Probe runs a synthetic triage through every AI chain entry at once (settings "test" button), so
// the owner sees which keys work rather than only whether the chain as a whole does.
func (s *TriageService) Probe(ctx context.Context) ([]ProbeResult, error) {
	st := s.settings.Get()
	if len(st.AIChain) == 0 {
		return nil, domain.ErrProviderUnset
	}
	now := time.Now()
	in := ai.TriageInput{
		OwnerName:   "Владелец",
		OwnerAbout:  s.cfg.OwnerAbout,
		Now:         now,
		Location:    s.cfg.Location,
		Sensitivity: st.Sensitivity,
		ContactName: "Тестовый собеседник",
		New: []domain.Message{
			{Text: "Привет! Слушай, у нас после вчерашнего релиза не работает вход в админку — отдаёт 500.", SentAt: now.Add(-40 * time.Second)},
			{Text: "Можешь глянуть до конца дня? Клиенты жалуются", SentAt: now.Add(-20 * time.Second)},
		},
	}
	req := ai.Request{System: ai.SystemPrompt(in), User: ai.UserPrompt(in), Schema: ai.AnalysisSchema()}
	results := make([]ProbeResult, len(st.AIChain))
	var wg sync.WaitGroup
	for i, entry := range st.AIChain {
		wg.Go(func() { results[i] = s.probeEntry(ctx, i, entry, st.ModelFor(entry.Provider), req) })
	}
	wg.Wait()
	return results, nil
}

func (s *TriageService) probeEntry(ctx context.Context, i int, entry domain.AIKey, model string, req ai.Request) ProbeResult {
	res := ProbeResult{Index: i + 1, Provider: entry.Provider, Model: model, KeyMask: domain.MaskKey(entry.Key)}
	p, ok := s.registry.Get(entry.Provider)
	if !ok {
		res.Err = domain.ErrProviderUnset
		return res
	}
	req.APIKey, req.Model = entry.Key, model
	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	resp, err := p.Complete(callCtx, req)
	res.Latency = time.Since(started)
	if err != nil {
		res.Err = err
		return res
	}
	res.Analysis, res.Err = ai.ParseAnalysis(resp.Text)
	return res
}

func batchText(msgs []domain.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "\n")
}

func telegramIDs(msgs []domain.Message) []int {
	ids := make([]int, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.MessageID)
	}
	return ids
}
