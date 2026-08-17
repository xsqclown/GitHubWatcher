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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase = "https://api.telegram.org"

	maxMessageLen = 3800

	maxSendAttempts = 4
)

// Target is one notification recipient. ChatID may point at a direct message
// with a person, a plain group, or a forum supergroup; TopicID is only set for
// forums and selects the topic inside one.
type Target struct {
	ChatID  int64
	TopicID int64
}

// String renders the recipient compactly for logs: "-1001234567890:42".
func (t Target) String() string {
	if t.TopicID != 0 {
		return strconv.FormatInt(t.ChatID, 10) + ":" + strconv.FormatInt(t.TopicID, 10)
	}
	return strconv.FormatInt(t.ChatID, 10)
}

// Client sends messages through the Telegram Bot API. Sends are serialised by a
// mutex and spaced out in time so the bot stays inside the API rate limits.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
	targets []Target
	silent  bool
	log     *slog.Logger

	mu       sync.Mutex
	interval time.Duration
	nextSend time.Time
	broken   map[Target]struct{}
}

// Options configures New.
type Options struct {
	Token    string
	Targets  []Target
	Silent   bool
	Interval time.Duration
	Timeout  time.Duration
	Logger   *slog.Logger
}

// New creates a Telegram Bot API client.
func New(o Options) *Client {
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	return &Client{
		http:     &http.Client{Timeout: o.Timeout},
		baseURL:  apiBase,
		token:    o.Token,
		targets:  o.Targets,
		silent:   o.Silent,
		interval: o.Interval,
		log:      o.Logger,
		broken:   map[Target]struct{}{},
	}
}

// APIError is a rejection returned by the Bot API itself.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

// Fatal reports that retrying is pointless: the problem is the token, the
// permissions or the request itself rather than a transient failure.
func (e *APIError) Fatal() bool {
	switch e.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		return true
	}
	return false
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type sendMessageRequest struct {
	ChatID              int64               `json:"chat_id"`
	MessageThreadID     int64               `json:"message_thread_id,omitempty"`
	Text                string              `json:"text"`
	ParseMode           string              `json:"parse_mode"`
	DisableNotification bool                `json:"disable_notification,omitempty"`
	LinkPreviewOptions  *linkPreviewOptions `json:"link_preview_options,omitempty"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

// BotInfo is what getMe reports about the bot.
type BotInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// VerifyBot validates the token and returns the bot's identity.
func (c *Client) VerifyBot(ctx context.Context) (*BotInfo, error) {
	raw, err := c.call(ctx, "getMe", struct{}{})
	if err != nil {
		return nil, err
	}
	var info BotInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("telegram: parse getMe response: %w", err)
	}
	return &info, nil
}

// Send fans the text out to every recipient, splitting it to fit Telegram's
// message limit. A recipient that answers with a permanent error (bot blocked,
// removed from the chat, topic deleted) is disabled until restart so it cannot
// hold up the others. A transient error is returned to the caller, which
// retries the whole event next cycle — so recipients that already received it
// may see a duplicate.
func (c *Client) Send(ctx context.Context, text string) error {
	parts := splitMessage(text, maxMessageLen)

	var (
		delivered int
		skipped   int
		firstErr  error
	)
	for _, target := range c.targets {
		if c.isBroken(target) {
			skipped++
			continue
		}

		err := c.sendParts(ctx, target, parts)
		switch {
		case err == nil:
			delivered++
		case ctx.Err() != nil:
			return err
		case isFatal(err):
			c.markBroken(target)
			skipped++
			c.log.Error("recipient disabled until restart", "target", target, "err", err)
		default:
			if firstErr == nil {
				firstErr = err
			}
			c.log.Warn("delivery to recipient failed", "target", target, "err", err)
		}
	}

	if firstErr != nil {
		return firstErr
	}
	if delivered == 0 {
		return fmt.Errorf("telegram: no reachable recipients (%d disabled)", skipped)
	}
	return nil
}

func (c *Client) sendParts(ctx context.Context, target Target, parts []string) error {
	for _, part := range parts {
		if err := c.sendOne(ctx, target, part); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendOne(ctx context.Context, target Target, text string) error {
	req := sendMessageRequest{
		ChatID:              target.ChatID,
		MessageThreadID:     target.TopicID,
		Text:                text,
		ParseMode:           "HTML",
		DisableNotification: c.silent,
		LinkPreviewOptions:  &linkPreviewOptions{IsDisabled: true},
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if err := c.waitTurn(ctx); err != nil {
			return err
		}

		_, err := c.call(ctx, "sendMessage", req)
		c.nextSend = time.Now().Add(c.interval)
		if err == nil {
			return nil
		}

		var apiErr *APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.RetryAfter > 0:
			c.log.Warn("telegram asked us to back off", "retry_after", apiErr.RetryAfter, "attempt", attempt, "target", target)
			c.nextSend = time.Now().Add(apiErr.RetryAfter)
		case errors.As(err, &apiErr) && apiErr.Fatal():
			return err
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err
		default:
			c.nextSend = time.Now().Add(backoff(attempt))
		}

		if attempt == maxSendAttempts {
			return fmt.Errorf("telegram: giving up after %d attempts: %w", maxSendAttempts, err)
		}
	}
	return nil
}

func (c *Client) isBroken(t Target) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.broken[t]
	return ok
}

func (c *Client) markBroken(t Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.broken[t] = struct{}{}
}

func (c *Client) waitTurn(ctx context.Context) error {
	d := time.Until(c.nextSend)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("telegram: encode %s request: %w", method, err)
	}

	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("telegram: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: %s: %w", method, redactToken(err, c.token))
	}
	defer resp.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("telegram: %s: decode response (%d): %w", method, resp.StatusCode, err)
	}
	if !parsed.OK {
		apiErr := &APIError{Code: parsed.ErrorCode, Description: parsed.Description}
		if apiErr.Code == 0 {
			apiErr.Code = resp.StatusCode
		}
		if parsed.Parameters != nil && parsed.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(parsed.Parameters.RetryAfter) * time.Second
		}
		return nil, apiErr
	}
	return parsed.Result, nil
}

func isFatal(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Fatal()
}

func redactToken(err error, token string) error {
	if token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "***")
	return errors.New(msg)
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

func splitMessage(text string, limit int) []string {
	if len([]rune(text)) <= limit {
		return []string{text}
	}

	var (
		parts []string
		buf   strings.Builder
		size  int
	)
	flush := func() {
		if buf.Len() > 0 {
			parts = append(parts, strings.TrimRight(buf.String(), "\n"))
			buf.Reset()
			size = 0
		}
	}

	for _, line := range strings.Split(text, "\n") {
		lineLen := len([]rune(line)) + 1
		if lineLen > limit {
			flush()
			for _, chunk := range chunkRunes(line, limit) {
				parts = append(parts, chunk)
			}
			continue
		}
		if size+lineLen > limit {
			flush()
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		size += lineLen
	}
	flush()
	return parts
}

func chunkRunes(s string, limit int) []string {
	runes := []rune(s)
	var out []string
	for len(runes) > limit {
		out = append(out, string(runes[:limit]))
		runes = runes[limit:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}
