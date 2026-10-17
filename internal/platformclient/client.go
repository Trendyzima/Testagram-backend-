// Package platformclient provides implementation-independent access to Testagram's
// internal cache, rate-limit, and queue API. Application services depend on these
// contracts rather than importing Valkey/Redis clients directly.
package platformclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrNotFound = errors.New("platform value not found")

type Cache interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string, time.Duration) error
	Delete(context.Context, string) error
}

type RateLimiter interface {
	Check(context.Context, string, int64, time.Duration) (RateLimitResult, error)
}

type Queue interface {
	Enqueue(context.Context, string, map[string]string) (string, error)
}

type RateLimitResult struct {
	Allowed          bool  `json:"allowed"`
	Count            int64 `json:"count"`
	Remaining        int64 `json:"remaining"`
	RetryAfterSeconds int64 `json:"retry_after_seconds"`
}

type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("Testagram platform API returned HTTP %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("platform base URL must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("platform internal token is required")
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: timeout}}, nil
}

func (c *Client) Get(ctx context.Context, key string) (string, error) {
	q := url.Values{"key": []string{key}}
	var result struct { Value string `json:"value"` }
	err := c.do(ctx, http.MethodGet, "/v1/cache/get?"+q.Encode(), nil, &result)
	if status, ok := err.(*StatusError); ok && status.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if err != nil { return "", err }
	return result.Value, nil
}

func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 { seconds++ }
	if seconds < 1 || seconds > 86400 { return errors.New("cache TTL must be between 1 second and 24 hours") }
	return c.do(ctx, http.MethodPost, "/v1/cache/set", map[string]any{"key": key, "value": value, "ttl_seconds": seconds}, nil)
}

func (c *Client) Delete(ctx context.Context, key string) error {
	q := url.Values{"key": []string{key}}
	return c.do(ctx, http.MethodDelete, "/v1/cache/delete?"+q.Encode(), nil, nil)
}

func (c *Client) Check(ctx context.Context, key string, limit int64, window time.Duration) (RateLimitResult, error) {
	seconds := int64(window / time.Second)
	if window%time.Second != 0 { seconds++ }
	if limit < 1 || limit > 100000 || seconds < 1 || seconds > 86400 {
		return RateLimitResult{}, errors.New("rate-limit limit/window is outside supported bounds")
	}
	var result RateLimitResult
	err := c.do(ctx, http.MethodPost, "/v1/rate-limit/check", map[string]any{"key": key, "limit": limit, "window_seconds": seconds}, &result)
	return result, err
}

func (c *Client) Enqueue(ctx context.Context, queue string, payload map[string]string) (string, error) {
	if len(payload) == 0 { return "", errors.New("queue payload must not be empty") }
	var result struct { ID string `json:"id"` }
	err := c.do(ctx, http.MethodPost, "/v1/queue/enqueue", map[string]any{"queue": queue, "payload": payload}, &result)
	return result.ID, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil { return err }
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil { return err }
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := c.http.Do(req)
	if err != nil { return fmt.Errorf("call Testagram platform API: %w", err) }
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil { return err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusNotFound {
			return &StatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
		}
		return &StatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent || len(raw) == 0 { return nil }
	if err := json.Unmarshal(raw, out); err != nil { return fmt.Errorf("decode Testagram platform response: %w", err) }
	return nil
}

