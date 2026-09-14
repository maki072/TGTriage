// Package gemini is the Google Gemini adapter (generateContent with JSON schema output).
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
	"tgtriage/internal/netutil"
)

// Config of the Gemini adapter.
type Config struct {
	APIKey      string
	BaseURL     string // default https://generativelanguage.googleapis.com
	MaxTokens   int    // default 8192 (thinking models spend part of it on reasoning)
	Temperature float64
	Timeout     time.Duration
	Socks5Addr  string // e.g. "127.0.0.1:1080"; empty = dial directly
}

// Provider implements ai.Provider for Gemini.
type Provider struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 8192
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Provider{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout, Transport: netutil.NewTransport(cfg.Socks5Addr)}}
}

func (p *Provider) Name() string { return domain.ProviderGemini }

type part struct {
	Text    string `json:"text,omitempty"`
	Thought bool   `json:"thought,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type generateRequest struct {
	SystemInstruction *content       `json:"systemInstruction,omitempty"`
	Contents          []content      `json:"contents"`
	GenerationConfig  map[string]any `json:"generationConfig"`
}

type generateResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

type errorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (p *Provider) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if p.cfg.APIKey == "" {
		return nil, &ai.Error{Provider: p.Name(), Message: "GEMINI_API_KEY is not set"}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.cfg.MaxTokens
	}
	body := generateRequest{
		SystemInstruction: &content{Parts: []part{{Text: req.System}}},
		Contents:          []content{{Role: "user", Parts: []part{{Text: req.User}}}},
		GenerationConfig: map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": req.Schema,
			"temperature":        p.cfg.Temperature,
			"maxOutputTokens":    maxTokens,
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent", p.cfg.BaseURL, url.PathEscape(req.Model))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.cfg.APIKey)

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("gemini read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var er errorResponse
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &er) == nil && er.Error.Message != "" {
			msg = er.Error.Status + ": " + er.Error.Message
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return nil, &ai.Error{Provider: p.Name(), Status: resp.StatusCode, Message: msg, Retryable: ai.RetryableStatus(resp.StatusCode)}
	}

	var gr generateResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, &ai.Error{Provider: p.Name(), Message: "decode response: " + err.Error()}
	}
	if gr.PromptFeedback != nil && gr.PromptFeedback.BlockReason != "" {
		return nil, &ai.Error{Provider: p.Name(), Message: "prompt blocked: " + gr.PromptFeedback.BlockReason}
	}
	if len(gr.Candidates) == 0 {
		return nil, &ai.Error{Provider: p.Name(), Message: "no candidates in response", Retryable: true}
	}
	cand := gr.Candidates[0]
	var sb strings.Builder
	for _, pt := range cand.Content.Parts {
		if !pt.Thought {
			sb.WriteString(pt.Text)
		}
	}
	switch cand.FinishReason {
	case "", "STOP":
	case "MAX_TOKENS":
		return nil, &ai.Error{Provider: p.Name(), Message: "response truncated by maxOutputTokens"}
	default:
		if sb.Len() == 0 {
			return nil, &ai.Error{Provider: p.Name(), Message: "generation stopped: " + cand.FinishReason}
		}
	}
	if sb.Len() == 0 {
		return nil, &ai.Error{Provider: p.Name(), Message: "empty response content", Retryable: true}
	}
	return &ai.Response{
		Text:         sb.String(),
		Model:        firstNonEmpty(gr.ModelVersion, req.Model),
		InputTokens:  gr.UsageMetadata.PromptTokenCount,
		OutputTokens: gr.UsageMetadata.CandidatesTokenCount,
	}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
