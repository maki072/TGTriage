// Package openaicompat is a single adapter for every provider that speaks the OpenAI Chat
// Completions wire format with json_schema structured outputs — Groq, Mistral, OpenRouter, and
// any future one like them. Providers differ only in name, base URL, and default model, so one
// implementation avoids three (and counting) near-identical copies.
package openaicompat

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
	"tgtriage/internal/netutil"
)

// Config of an OpenAI-compatible adapter instance.
type Config struct {
	Name        string // domain.Provider* constant — identifies this instance and is used in errors
	BaseURL     string // e.g. "https://api.groq.com/openai/v1" (no trailing slash needed)
	MaxTokens   int    // default 8192
	Temperature float64
	Timeout     time.Duration     // default 90s
	Socks5Addr  string            // e.g. "127.0.0.1:1080"; empty = dial directly
	Headers     map[string]string // extra request headers some gateways want (e.g. OpenRouter attribution)
}

// Provider implements ai.Provider for any OpenAI-compatible Chat Completions endpoint.
type Provider struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Provider {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 8192
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Provider{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout, Transport: netutil.NewTransport(cfg.Socks5Addr)}}
}

func (p *Provider) Name() string { return p.cfg.Name }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchemaFormat struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

type responseFormat struct {
	Type       string           `json:"type"`
	JSONSchema jsonSchemaFormat `json:"json_schema"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
	MaxTokens      int            `json:"max_tokens"`
	Temperature    float64        `json:"temperature"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		// Code varies by provider (Groq/Mistral send a string, OpenRouter a number) and is never
		// read — json.RawMessage accepts either shape without failing the whole unmarshal.
		Code json.RawMessage `json:"code"`
	} `json:"error"`
}

func (p *Provider) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if req.APIKey == "" {
		return nil, &ai.Error{Provider: p.Name(), Message: "API key is not set"}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.cfg.MaxTokens
	}
	body := chatRequest{
		Model: req.Model,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		ResponseFormat: responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchemaFormat{
				Name:   "task_analysis",
				Schema: req.Schema,
				Strict: true,
			},
		},
		MaxTokens:   maxTokens,
		Temperature: p.cfg.Temperature,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	for k, v := range p.cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", p.Name(), err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s read body: %w", p.Name(), err)
	}
	if resp.StatusCode != http.StatusOK {
		var er errorResponse
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &er) == nil && er.Error.Message != "" {
			msg = er.Error.Message
			if er.Error.Type != "" {
				msg = er.Error.Type + ": " + msg
			}
		}
		return nil, &ai.Error{Provider: p.Name(), Status: resp.StatusCode, Message: truncate(msg, 500), Retryable: ai.RetryableStatus(resp.StatusCode)}
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, &ai.Error{Provider: p.Name(), Message: "decode response: " + err.Error()}
	}
	if len(cr.Choices) == 0 {
		return nil, &ai.Error{Provider: p.Name(), Message: "no choices in response", Retryable: true}
	}
	choice := cr.Choices[0]
	if choice.FinishReason == "length" {
		return nil, &ai.Error{Provider: p.Name(), Message: "response truncated by max_tokens", Retryable: false}
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return nil, &ai.Error{Provider: p.Name(), Message: "empty response content", Retryable: true}
	}
	return &ai.Response{
		Text:         choice.Message.Content,
		Model:        firstNonEmpty(cr.Model, req.Model),
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
	}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
