// Package platformclient provides implementation-independent access to Testagram's
// internal cache, rate-limit, and queue API. Application services depend on these
// contracts rather than importing Valkey/Redis clients directly.
package platformclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && isPrivateHost(u.Hostname()))) {
		return nil, errors.New("platform base URL must use HTTPS or a private HTTP service hostname")
	}
	token = strings.TrimSpace(token)
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("platform internal token must contain at least 32 characters")
	}
	if timeout <= 0 || timeout > 10*time.Second { timeout = 2*time.Second }
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{baseURL: baseURL, token: token, http: client}, nil
}

func isPrivateHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || host == "platform" || host == "api" || strings.HasSuffix(host, ".local") { return true }
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil { return hex.EncodeToString(raw[:]) }
	return fmt.Sprintf("platform-%d", time.Now().UnixNano())
}

func validateKey(key string) error {
	if len(key) < 1 || len(key) > 160 {
		return errors.New("platform key must contain between 1 and 160 ASCII characters")
	}
	for _, char := range key {
		if !(char == '-' || char == '_' || char == '.' || char == ':' ||
			(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9')) {
			return errors.New("platform key contains a character not supported by the API")
		}
	}
	return nil
}

func (c *Client) Get(ctx context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil { return "", err }
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
	if err := validateKey(key); err != nil { return err }
	if len(value) > 256<<10 { return errors.New("cache value exceeds 256 KiB") }
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 { seconds++ }
	if seconds < 1 || seconds > 86400 { return errors.New("cache TTL must be between 1 second and 24 hours") }
	return c.do(ctx, http.MethodPost, "/v1/cache/set", map[string]any{"key": key, "value": value, "ttl_seconds": seconds}, nil)
}

func (c *Client) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil { return err }
	q := url.Values{"key": []string{key}}
	return c.do(ctx, http.MethodDelete, "/v1/cache/delete?"+q.Encode(), nil, nil)
}

func (c *Client) Check(ctx context.Context, key string, limit int64, window time.Duration) (RateLimitResult, error) {
	if err := validateKey(key); err != nil { return RateLimitResult{}, err }
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
	if err := validateKey(queue); err != nil { return "", fmt.Errorf("invalid queue name: %w", err) }
	if len(payload) == 0 || len(payload) > 256 { return "", errors.New("queue payload must contain between 1 and 256 fields") }
	for key, value := range payload {
		if validateKey(key) != nil || len(value) > 16<<10 { return "", errors.New("queue payload contains an invalid field or oversized value") }
	}
	var result struct { ID string `json:"id"` }
	err := c.do(ctx, http.MethodPost, "/v1/queue/enqueue", map[string]any{"queue": queue, "payload": payload}, &result)
	if err != nil { return "", err }
	if strings.TrimSpace(result.ID) == "" || len(result.ID) > 128 { return "", errors.New("platform API returned an invalid queue job ID") }
	return result.ID, nil
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
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	req.Header.Set("X-Request-ID", newRequestID())
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := c.http.Do(req)
	if err != nil { return fmt.Errorf("call Testagram platform API: %w", err) }
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil { return fmt.Errorf("read platform API response: %w", err) }
	if len(raw) > 1<<20 { return errors.New("platform API response exceeded 1 MiB") }
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

