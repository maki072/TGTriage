package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"tgtriage/internal/netutil"
)

// APIError is an error returned by the Bot API.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// IsNotModified reports the harmless "message is not modified" edit error.
func IsNotModified(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified")
}

// IsEntityError reports HTML parse / URL errors that can be fixed by re-rendering.
func IsEntityError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != http.StatusBadRequest {
		return false
	}
	d := strings.ToLower(ae.Description)
	return strings.Contains(d, "parse entities") || strings.Contains(d, "url") || strings.Contains(d, "entity")
}

// Client is a Telegram Bot API client.
type Client struct {
	token    string
	baseURL  string
	http     *http.Client
	pollHTTP *http.Client
	log      *slog.Logger
}

// NewClient creates a client. baseURL defaults to https://api.telegram.org.
// socks5Addr, when non-empty (e.g. "127.0.0.1:1080"), routes every request through that
// unauthenticated SOCKS5 proxy — for hosts where Telegram itself is blocked/censored but the
// rest of the internet (LLM providers, etc.) is directly reachable, so only this client needs
// a bypass rather than the whole process's outbound traffic.
func NewClient(token, baseURL, socks5Addr string, log *slog.Logger) *Client {
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	transport := netutil.NewTransport(socks5Addr)
	return &Client{
		token:    token,
		baseURL:  strings.TrimRight(baseURL, "/"),
		http:     &http.Client{Timeout: 30 * time.Second, Transport: transport},
		pollHTTP: &http.Client{Timeout: 70 * time.Second, Transport: transport},
		log:      log,
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call performs a Bot API method; retries only on 429 (guaranteed not processed by Telegram).
func (c *Client) call(ctx context.Context, hc *http.Client, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", method, err)
	}
	const maxAttempts = 4
	for attempt := 1; ; attempt++ {
		err := c.do(ctx, hc, method, body, out)
		var ae *APIError
		if errors.As(err, &ae) && ae.Code == http.StatusTooManyRequests && attempt < maxAttempts {
			wait := time.Duration(max(ae.RetryAfter, 1)) * time.Second
			c.log.Warn("telegram rate limited", "method", method, "retry_after", wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				continue
			}
		}
		return err
	}
}

func (c *Client) do(ctx context.Context, hc *http.Client, method string, body []byte, out any) error {
	url := c.baseURL + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return c.sanitize(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return c.sanitize(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return c.sanitize(err)
	}
	var ar apiResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("telegram %s: bad response (http %d): %w", method, resp.StatusCode, err)
	}
	if !ar.OK {
		ae := &APIError{Method: method, Code: ar.ErrorCode, Description: ar.Description}
		if ar.Parameters != nil {
			ae.RetryAfter = ar.Parameters.RetryAfter
		}
		return ae
	}
	if out != nil {
		if err := json.Unmarshal(ar.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

// sanitize removes the bot token from transport errors (net/http includes the URL).
func (c *Client) sanitize(err error) error {
	if err == nil || c.token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.token, "<token>")
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", msg, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", msg, context.DeadlineExceeded)
	}
	return errors.New(msg)
}

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.call(ctx, c.http, "getMe", struct{}{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, c.http, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int, allowed []string) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, c.pollHTTP, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": allowed,
	}, &ups)
	return ups, err
}

func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (*Message, error) {
	var m Message
	if err := c.call(ctx, c.http, "sendMessage", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) EditMessageText(ctx context.Context, p EditMessageTextParams) error {
	return c.call(ctx, c.http, "editMessageText", p, nil)
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	return c.call(ctx, c.http, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
		"show_alert":        alert,
	}, nil)
}

// ReadBusinessMessage marks an incoming message as read on behalf of the business account.
func (c *Client) ReadBusinessMessage(ctx context.Context, connectionID string, chatID int64, messageID int) error {
	return c.call(ctx, c.http, "readBusinessMessage", map[string]any{
		"business_connection_id": connectionID,
		"chat_id":                chatID,
		"message_id":             messageID,
	}, nil)
}

func (c *Client) GetBusinessConnection(ctx context.Context, id string) (*BusinessConnection, error) {
	var bc BusinessConnection
	if err := c.call(ctx, c.http, "getBusinessConnection", map[string]any{"business_connection_id": id}, &bc); err != nil {
		return nil, err
	}
	return &bc, nil
}

func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, c.http, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

// Poll runs long polling until ctx is cancelled. Updates are handled sequentially.
func (c *Client) Poll(ctx context.Context, handler func(context.Context, Update)) {
	if err := c.DeleteWebhook(ctx); err != nil {
		c.log.Warn("deleteWebhook failed", "err", err)
	}
	var offset int64
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		ups, err := c.GetUpdates(ctx, offset, 50, AllowedUpdates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Error("getUpdates failed", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			handler(ctx, u)
			offset = u.UpdateID + 1
		}
	}
}
