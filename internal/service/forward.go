package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
)

// forwardDebounce glues messages the owner forwards in one go: Telegram delivers a multi-message
// forward as separate updates a fraction of a second apart.
const forwardDebounce = 2 * time.Second

// OnForwarded buffers a message the owner forwarded to the bot from another chat (someone else's or
// their own; m.Outgoing marks the latter). After forwardDebounce of quiet the batch becomes one task.
// Forwarding is an explicit request, so it bypasses the triage pause, the noise prefilter and the
// confidence threshold.
func (s *TriageService) OnForwarded(m domain.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fwd = append(s.fwd, m)
	if s.fwdTimer != nil {
		s.fwdTimer.Stop()
	}
	s.gen++
	gen := s.gen
	s.fwdGen = gen
	s.fwdTimer = time.AfterFunc(forwardDebounce, func() { s.flushForwarded(gen) })
}

func (s *TriageService) flushForwarded(gen uint64) {
	s.mu.Lock()
	if gen != s.fwdGen || len(s.fwd) == 0 {
		s.mu.Unlock()
		return
	}
	msgs := s.fwd
	s.fwd, s.fwdTimer = nil, nil
	s.mu.Unlock()
	if s.baseCtx.Err() != nil {
		return // shutting down; forwards are not persisted, the owner can simply forward again
	}
	s.wg.Add(1)
	defer s.wg.Done()
	log := s.log.With("forwarded", len(msgs))
	s.runDetached(log, func(ctx context.Context) error { return s.analyzeForwarded(ctx, msgs, log) })
}

func (s *TriageService) analyzeForwarded(ctx context.Context, msgs []domain.Message, log *slog.Logger) error {
	st := s.settings.Get()
	rec := &domain.AnalysisRecord{InputText: forwardedText(msgs)}

	var ownerName string
	if conn, err := s.conns.Current(ctx); err == nil {
		ownerName = conn.UserName
	}
	loc := s.settings.Location()
	in := ai.ForwardInput{
		OwnerName:  ownerName,
		OwnerAbout: st.OwnerAbout,
		Now:        time.Now(),
		Location:   loc,
		Messages:   msgs,
	}
	req := ai.Request{System: ai.ForwardSystemPrompt(in), User: ai.ForwardUserPrompt(in), Schema: ai.AnalysisSchema()}

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
		return s.forwardFailed(ctx, rec, err)
	}
	analysis := res.Analysis

	rec.Status = domain.AnalysisOK
	rec.IsTask = true // the owner has already decided
	rec.Confidence = analysis.Confidence
	rec.MessageType = analysis.MessageType
	if err := s.analyses.Create(ctx, rec); err != nil {
		return err
	}

	// The task is attributed to the first author other than the owner; a note-to-self keeps the owner.
	sender := msgs[0]
	for _, m := range msgs {
		if !m.Outgoing {
			sender = m
			break
		}
	}
	// No ConnectionID/ChatID/SourceMessageIDs: the source chat is unknown, so the task cannot be replied to.
	t := &domain.Task{
		SenderID:       sender.SenderID,
		SenderName:     sender.SenderName,
		SenderUsername: sender.SenderUsername,
		SourceText:     rec.InputText,
		Title:          analysis.Title,
		Description:    analysis.Description,
		Priority:       domain.ParsePriority(analysis.Priority),
		Category:       domain.ParseCategory(analysis.Category),
		Deadline:       ai.ParseDeadline(analysis.Deadline, loc),
		Confidence:     analysis.Confidence,
		Status:         domain.StatusNew,
		AnalysisID:     rec.ID,
		Provider:       rec.Provider,
		Model:          rec.Model,
	}
	if t.Title == "" {
		t.Title = fallbackTitle(msgs[0].Text)
	}
	if err := s.tasks.Create(ctx, t); err != nil {
		return err
	}
	if err := s.analyses.SetTaskID(ctx, rec.ID, t.ID); err != nil {
		log.Warn("link analysis to task", "err", err)
	}
	log.Info("task created from forwarded messages", "task_id", t.ID, "provider", rec.Provider,
		"model", rec.Model, "latency_ms", rec.LatencyMs)
	s.notifier.TaskCreated(ctx, t)
	return nil
}

func (s *TriageService) forwardFailed(ctx context.Context, rec *domain.AnalysisRecord, err error) error {
	rec.Status = domain.AnalysisError
	rec.Error = err.Error()
	if cerr := s.analyses.Create(ctx, rec); cerr != nil {
		err = errors.Join(err, cerr)
	}
	s.notifier.ForwardFailed(ctx, rec)
	return err
}

// forwardedText joins forwarded messages; authors are named only when the batch mixes several of them.
func forwardedText(msgs []domain.Message) string {
	mixed := false
	for _, m := range msgs {
		mixed = mixed || m.SenderName != msgs[0].SenderName
	}
	if !mixed {
		return batchText(msgs)
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.SenderName+": "+m.Text)
	}
	return strings.Join(parts, "\n")
}

func fallbackTitle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return line
}
