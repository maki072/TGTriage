package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
)

// Style learning: draft replies imitate how the owner writes. Two cheap ingredients, both built
// from the owner's own messages already stored for context:
//   - verbatim examples from the same chat, read straight from the DB on every analysis (no LLM);
//   - style profiles — one general and one per busiest contact — that a background job asks the
//     LLM for once a week (at most 1+styleChatProfiles calls).
const (
	styleGlobalKey      = "style_global"
	styleAtKey          = "style_at"
	styleRefreshEvery   = 7 * 24 * time.Hour
	styleCheckEvery     = 6 * time.Hour
	styleMinReplies     = 30  // total replies before a general profile makes sense
	styleChatMinReplies = 20  // replies in one chat before it gets its own profile
	styleChatProfiles   = 5   // busiest contacts with their own profile
	styleTopUp          = 6   // examples from other chats when this chat has fewer than this many
	styleSampleGlobal   = 120 // replies shown to the model for the general profile
	styleSamplePerChat  = 12  // ...at most this many from a single chat, so one chat does not dominate
	styleSampleChat     = 60  // replies shown to the model for a contact's profile
)

func styleChatKey(connID string, chatID int64) string {
	return fmt.Sprintf("style_chat_%s_%d", connID, chatID)
}

// styleFor collects what the model should know about the owner's manner in this chat. before is
// the row id where the prompt's context window starts, so examples do not repeat it.
func (s *TriageService) styleFor(ctx context.Context, st domain.Settings, connID string, chatID, before int64) ai.Style {
	if !st.StyleLearning {
		return ai.Style{}
	}
	var style ai.Style
	style.Profile, _ = s.settings.Meta(ctx, styleGlobalKey)
	style.ChatProfile, _ = s.settings.Meta(ctx, styleChatKey(connID, chatID))
	if st.StyleExamples <= 0 {
		return style
	}
	own, err := s.messages.OwnerReplies(ctx, domain.ReplyQuery{
		ConnectionID: connID, ChatID: chatID, BeforeID: before, Limit: st.StyleExamples})
	if err != nil {
		s.log.Warn("style: load examples", "chat_id", chatID, "err", err)
	}
	style.Examples = replyTexts(own)
	if need := styleTopUp - len(own); need > 0 {
		other, err := s.messages.OwnerReplies(ctx, domain.ReplyQuery{ExceptChatID: chatID, Limit: need * 3})
		if err != nil {
			s.log.Warn("style: load examples of other chats", "err", err)
		}
		texts := replyTexts(other)
		style.OtherExamples = texts[:min(need, len(texts))]
	}
	return style
}

// replyTexts returns the distinct texts of msgs in order.
func replyTexts(msgs []domain.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if !slices.Contains(out, m.Text) {
			out = append(out, m.Text)
		}
	}
	return out
}

// styleLoop refreshes the style profiles about once a week for as long as ctx lives.
func (s *TriageService) styleLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	t := time.NewTicker(styleCheckEvery)
	defer t.Stop()
	for {
		s.refreshStyle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *TriageService) refreshStyle(ctx context.Context) {
	log := s.log.With("job", "style")
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in style refresh", "panic", r)
		}
	}()
	st := s.settings.Get()
	if !st.StyleLearning || len(st.AIChain) == 0 {
		return
	}
	if at, _ := s.settings.Meta(ctx, styleAtKey); at != "" {
		if t, err := time.Parse(time.RFC3339, at); err == nil && time.Since(t) < styleRefreshEvery {
			return
		}
	}
	replies, err := s.messages.OwnerReplies(ctx, domain.ReplyQuery{Limit: 500})
	if err != nil {
		log.Error("load replies", "err", err)
		return
	}
	if len(replies) < styleMinReplies {
		return // too little to learn from yet; checked again on the next tick
	}
	if !s.saveStyleProfile(ctx, st, styleGlobalKey, true, spreadReplies(replies, styleSampleGlobal, styleSamplePerChat), log) {
		return
	}
	chats, err := s.messages.TopReplyChats(ctx, styleChatMinReplies, styleChatProfiles)
	if err != nil {
		log.Error("load busiest chats", "err", err)
	}
	for _, c := range chats {
		msgs, err := s.messages.OwnerReplies(ctx, domain.ReplyQuery{ConnectionID: c.ConnectionID, ChatID: c.ChatID, Limit: styleSampleChat})
		if err != nil {
			log.Warn("load chat replies", "chat_id", c.ChatID, "err", err)
			continue
		}
		s.saveStyleProfile(ctx, st, styleChatKey(c.ConnectionID, c.ChatID), false, reverse(replyTexts(msgs)), log)
	}
	if err := s.settings.SetMeta(ctx, styleAtKey, time.Now().Format(time.RFC3339)); err != nil {
		log.Error("save refresh time", "err", err)
	}
	log.Info("style profiles refreshed", "chats", len(chats))
}

// saveStyleProfile asks the AI chain to describe the manner in samples and stores the answer.
func (s *TriageService) saveStyleProfile(ctx context.Context, st domain.Settings, key string, general bool, samples []string, log *slog.Logger) bool {
	if len(samples) == 0 {
		return false
	}
	var profile string
	callCtx, cancel := context.WithTimeout(ctx, s.runBudget())
	defer cancel()
	req := ai.Request{System: ai.StyleProfileSystem(general), User: ai.StyleProfileUser(samples), Schema: ai.StyleProfileSchema(), MaxTokens: 2048}
	_, err := s.runChain(callCtx, st, req, s.log.With("job", "style"), func(text string) error {
		p, err := ai.ParseStyleProfile(text)
		profile = p
		return err
	})
	if err != nil {
		log.Warn("style profile failed", "key", key, "err", err)
		return false
	}
	if err := s.settings.SetMeta(ctx, key, profile); err != nil {
		log.Warn("save style profile", "key", key, "err", err)
		return false
	}
	return true
}

// spreadReplies picks up to total texts from newest-first msgs, at most perChat from any one chat,
// and returns them oldest first.
func spreadReplies(msgs []domain.Message, total, perChat int) []string {
	type chat struct {
		conn string
		id   int64
	}
	counts := map[chat]int{}
	var out []string
	for _, m := range msgs {
		k := chat{m.ConnectionID, m.ChatID}
		if counts[k] >= perChat || slices.Contains(out, m.Text) {
			continue
		}
		counts[k]++
		out = append(out, m.Text)
		if len(out) == total {
			break
		}
	}
	return reverse(out)
}

func reverse(s []string) []string {
	slices.Reverse(s)
	return s
}
