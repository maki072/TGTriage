// Package claude is the Anthropic Claude adapter (Messages API with structured outputs).
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tgtriage/internal/ai"
	"tgtriage/internal/domain"
	"tgtriage/internal/netutil"
)

const (
	apiVersion    = "2023-06-01"
	fallbacksBeta = "server-side-fallback-2026-07-01"
)

// Config of the Claude adapter.
type Config struct {
	BaseURL    string        // default https://api.anthropic.com
	Effort     string        // low|medium|high|xhigh|max; empty = API default
	Fallbacks  bool          // server-side refusal fallbacks for models that support them
	MaxTokens  int           // default 16000
	Timeout    time.Duration // default 90s
	Socks5Addr string        // e.g. "127.0.0.1:1080"; empty = dial directly
}

// Provider implements ai.Provider for Claude.
type Provider struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 16000
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Provider{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout, Transport: netutil.NewTransport(cfg.Socks5Addr)}}
}

func (p *Provider) Name() string { return domain.ProviderClaude }

type messageRequest struct {
	Model        string         `json:"model"`
	MaxTokens    int            `json:"max_tokens"`
	System       string         `json:"system,omitempty"`
	Messages     []message      `json:"messages"`
	OutputConfig map[string]any `json:"output_config,omitempty"`
	Fallbacks    string         `json:"fallbacks,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messageResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type errorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// supportsEffort: Haiku 4.5 and older models reject output_config.effort.
func supportsEffort(model string) bool {
	return !strings.Contains(model, "haiku") && !strings.Contains(model, "claude-3")
}

// supportsFallbacks: server-side refusal fallbacks apply to Opus 5 / Fable 5.1 tier models.
func supportsFallbacks(model string) bool {
	return strings.HasPrefix(model, "claude-opus-5") || strings.HasPrefix(model, "claude-fable-5-1")
}

func (p *Provider) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if req.APIKey == "" {
		return nil, &ai.Error{Provider: p.Name(), Message: "API key is not set"}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.cfg.MaxTokens
	}
	outCfg := map[string]any{
		"format": map[string]any{"type": "json_schema", "schema": req.Schema},
	}
	if p.cfg.Effort != "" && supportsEffort(req.Model) {
		outCfg["effort"] = p.cfg.Effort
	}
	body := messageRequest{
		Model:        req.Model,
		MaxTokens:    maxTokens,
		System:       req.System,
		Messages:     []message{{Role: "user", Content: req.User}},
		OutputConfig: outCfg,
	}
	useFallbacks := p.cfg.Fallbacks && supportsFallbacks(req.Model)
	if useFallbacks {
		body.Fallbacks = "default"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", req.APIKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	if useFallbacks {
		httpReq.Header.Set("anthropic-beta", fallbacksBeta)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("claude read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var er errorResponse
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &er) == nil && er.Error.Message != "" {
			msg = er.Error.Type + ": " + er.Error.Message
		}
		return nil, &ai.Error{Provider: p.Name(), Status: resp.StatusCode, Message: truncate(msg, 500), Retryable: ai.RetryableStatus(resp.StatusCode)}
	}

	var mr messageResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, &ai.Error{Provider: p.Name(), Message: "decode response: " + err.Error()}
	}
	switch mr.StopReason {
	case "refusal":
		return nil, &ai.Error{Provider: p.Name(), Message: "model refused the request"}
	case "max_tokens":
		return nil, &ai.Error{Provider: p.Name(), Message: "response truncated by max_tokens", Retryable: false}
	}
	var sb strings.Builder
	for _, c := range mr.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	if sb.Len() == 0 {
		return nil, &ai.Error{Provider: p.Name(), Message: "empty response content", Retryable: true}
	}
	return &ai.Response{
		Text:         sb.String(),
		Model:        mr.Model,
		InputTokens:  mr.Usage.InputTokens,
		OutputTokens: mr.Usage.OutputTokens,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
