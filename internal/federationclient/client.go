// Package federationclient calls the private Federation activity-ingress API.
// It is intended for a Backend transactional-outbox dispatcher; product request
// handlers must not synchronously couple a successful post write to remote peers.
package federationclient

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

type PublishRequest struct {
	ActivityID  string          `json:"activity_id"`
	ActorUserID string          `json:"actor_user_id"`
	Activity    json.RawMessage `json:"activity"`
}

type PublishResult struct {
	ActivityID     string `json:"activity_id"`
	RecipientCount int    `json:"recipient_count"`
	Accepted       bool   `json:"accepted"`
}

type envelope struct {
	OK        bool          `json:"ok"`
	Data      PublishResult `json:"data"`
	RequestID string        `json:"request_id"`
	Error     *struct {
		Code string `json:"code"`
	} `json:"error"`
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && isPrivateHost(u.Hostname()))) {
		return nil, errors.New("Federation URL must be HTTPS (HTTP only for private service hosts)")
	}
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Federation service token must contain at least 32 characters")
	}
	if timeout <= 0 || timeout > 8*time.Second {
		timeout = 3 * time.Second
	}
	return &Client{baseURL: baseURL, token: token, http: &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Publish(ctx context.Context, input PublishRequest) (PublishResult, error) {
	if c == nil {
		return PublishResult{}, errors.New("Federation client is not configured")
	}
	input.ActivityID = strings.TrimSpace(input.ActivityID)
	input.ActorUserID = strings.TrimSpace(input.ActorUserID)
	if input.ActivityID == "" || len(input.ActivityID) > 2048 ||
		input.ActorUserID == "" || len(input.ActorUserID) > 128 ||
		len(input.Activity) == 0 || len(input.Activity) > 1<<20 || !json.Valid(input.Activity) {
		return PublishResult{}, errors.New("invalid Federation publish request")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return PublishResult{}, fmt.Errorf("encode Federation activity: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/v1/activities", bytes.NewReader(body))
	if err != nil {
		return PublishResult{}, err
	}
	requestID := newRequestID()
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("Idempotency-Key", input.ActivityID)

	resp, err := c.http.Do(req)
	if err != nil {
		return PublishResult{}, fmt.Errorf("call Federation activity ingress: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return PublishResult{}, errors.New("Federation response is unreadable or too large")
	}
	if resp.StatusCode != http.StatusAccepted {
		return PublishResult{}, fmt.Errorf("Federation activity ingress returned HTTP %d", resp.StatusCode)
	}
	var result envelope
	if err := json.Unmarshal(raw, &result); err != nil || !result.OK || !result.Data.Accepted ||
		result.Data.ActivityID != input.ActivityID || result.Data.RecipientCount < 0 || result.Data.RecipientCount > 1000 {
		return PublishResult{}, errors.New("Federation returned an invalid activity acknowledgement")
	}
	return result.Data, nil
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func isPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".internal") || strings.EqualFold(host, "federation") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
