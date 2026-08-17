package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	acceptHeader   = "application/vnd.github+json"
	apiVersion     = "2022-11-28"
	userAgent      = "GithubPoller/1.0"
	maxAttempts    = 3
	maxResponseLen = 8 << 20
)

// ErrNotModified is returned when GitHub answers a conditional request with
// 304. Such a response costs no rate limit budget, which is why every poll
// sends the ETag it stored last time.
var ErrNotModified = errors.New("github: not modified (304)")

// RateLimitError means the request budget is exhausted. Primary distinguishes
// the hourly quota from a secondary abuse-protection limit.
type RateLimitError struct {
	ResetAt time.Time
	Primary bool
}

// Error implements the error interface.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: rate limit exceeded, resets in %s", time.Until(e.ResetAt).Truncate(time.Second))
}

// APIError is a non-successful HTTP response from the GitHub API.
type APIError struct {
	StatusCode int
	Message    string
	Path       string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("github: %s -> %d: %s", e.Path, e.StatusCode, e.Message)
}

// NotFound reports a 404. For a private repository this usually means the token
// cannot see it rather than that it does not exist.
func (e *APIError) NotFound() bool { return e.StatusCode == http.StatusNotFound }

// Client is a minimal GitHub REST API client with retries and ETag support.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
	log     *slog.Logger
}

// Response carries the response metadata callers need — currently just the
// ETag to replay on the next request.
type Response struct {
	ETag string
}

// New creates a GitHub API client. An empty token means unauthenticated access:
// 60 requests per hour and public repositories only.
func New(baseURL, token string, timeout time.Duration, log *slog.Logger) *Client {
	return &Client{
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
		baseURL: baseURL,
		token:   token,
		log:     log,
	}
}

func (c *Client) get(ctx context.Context, path string, query url.Values, etag string, out any) (*Response, error) {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := backoff(attempt)
			c.log.Debug("retrying GitHub request", "path", path, "attempt", attempt, "delay", delay)
			if err := sleepCtx(ctx, delay); err != nil {
				return nil, err
			}
		}

		resp, err := c.do(ctx, endpoint, etag, out)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		if !retryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s: out of retries: %w", path, lastErr)
}

func (c *Client) do(ctx context.Context, endpoint, etag string, out any) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &transportError{err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseLen))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, ErrNotModified

	case resp.StatusCode == http.StatusOK:
		body := io.LimitReader(resp.Body, maxResponseLen)
		if err := json.NewDecoder(body).Decode(out); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return &Response{ETag: resp.Header.Get("ETag")}, nil

	case isRateLimited(resp):
		return nil, rateLimitError(resp)

	default:
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    apiMessage(resp.Body),
			Path:       endpoint,
		}
	}
}

type transportError struct{ err error }

func (e *transportError) Error() string { return "github: transport: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func retryable(err error) bool {
	var te *transportError
	if errors.As(err, &te) {
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode >= 500
	}
	return false
}

func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode == http.StatusForbidden &&
		(resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "")
}

func rateLimitError(resp *http.Response) error {
	e := &RateLimitError{ResetAt: time.Now().Add(time.Minute), Primary: true}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			e.ResetAt = time.Unix(sec, 0)
		}
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		e.Primary = false
		if sec, err := strconv.Atoi(v); err == nil {
			e.ResetAt = time.Now().Add(time.Duration(sec) * time.Second)
		}
	}
	return e
}

func apiMessage(r io.Reader) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 64<<10)).Decode(&payload); err != nil || payload.Message == "" {
		return "unknown error"
	}
	return payload.Message
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
