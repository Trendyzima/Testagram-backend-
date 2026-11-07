package algorithmclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Features struct {
	Following        bool    `json:"following"`
	MutualAffinity   float64 `json:"mutualAffinity"`
	InterestAffinity float64 `json:"interestAffinity"`
	Quality          float64 `json:"quality"`
	Conversation     float64 `json:"conversation"`
	NegativeFeedback float64 `json:"negativeFeedback"`
	ReportRisk       float64 `json:"reportRisk"`
}

type Candidate struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"authorId"`
	CreatedAt time.Time `json:"createdAt"`
	Visible   bool      `json:"visible"`
	Sensitive bool      `json:"sensitive"`
	Features  Features  `json:"features"`
}

type Request struct {
	Now              time.Time   `json:"now"`
	Candidates       []Candidate `json:"candidates"`
	SeenIDs          []string    `json:"seenIds"`
	BlockedAuthorIDs []string    `json:"blockedAuthorIds"`
	MutedAuthorIDs   []string    `json:"mutedAuthorIds"`
	Limit            int         `json:"limit"`
	AllowSensitive   bool        `json:"allowSensitive"`
}

type RankedItem struct {
	ID      string   `json:"id"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
}

type Response struct {
	Items []RankedItem `json:"items"`
}

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func New(endpoint, token string, timeout time.Duration) (*Client, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	token = strings.TrimSpace(token)
	if endpoint == "" || token == "" {
		return nil, errors.New("algorithm endpoint and internal token are both required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("algorithm endpoint must be an absolute HTTP(S) origin without credentials, query or fragment")
	}
	if parsed.Scheme != "https" && !isTrustedPrivateHost(parsed.Hostname()) {
		return nil, errors.New("algorithm HTTP endpoint must use a private service hostname or private/loopback IP; use HTTPS for public hosts")
	}
	if len(token) < 32 {
		return nil, errors.New("algorithm internal token must contain at least 32 characters")
	}
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 1200 * time.Millisecond
	}
	return &Client{endpoint: endpoint, token: token, http: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Ready(ctx context.Context) error {
	if c == nil {
		return errors.New("algorithm client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("create algorithm readiness request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("algorithm readiness request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("algorithm readiness returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Rank(ctx context.Context, input Request) (Response, error) {
	if c == nil {
		return Response{}, errors.New("algorithm client is not configured")
	}
	if len(input.Candidates) == 0 || len(input.Candidates) > 200 {
		return Response{}, errors.New("algorithm candidate count must be between 1 and 200")
	}
	if input.Limit < 1 || input.Limit > 50 {
		return Response{}, errors.New("algorithm result limit must be between 1 and 50")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Response{}, fmt.Errorf("encode ranking request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/feed/rank", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("create ranking request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	req.Header.Set("X-Request-ID", newRequestID())
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("algorithm service request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Response{}, fmt.Errorf("algorithm service returned HTTP %d", resp.StatusCode)
	}
	var output Response
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<10))
	if err := decoder.Decode(&output); err != nil {
		return Response{}, fmt.Errorf("decode algorithm response: %w", err)
	}
	if len(output.Items) > input.Limit {
		return Response{}, errors.New("algorithm service exceeded requested result limit")
	}
	seen := make(map[string]struct{}, len(output.Items))
	for _, item := range output.Items {
		if strings.TrimSpace(item.ID) == "" || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) || item.Score < 0 || item.Score > 1 {
			return Response{}, errors.New("algorithm service returned an invalid ranked item")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return Response{}, errors.New("algorithm service returned a duplicate candidate")
		}
		seen[item.ID] = struct{}{}
		if len(item.Reasons) > 8 {
			return Response{}, errors.New("algorithm service returned too many ranking reasons")
		}
		for _, reason := range item.Reasons {
			if len(reason) == 0 || len(reason) > 64 {
				return Response{}, errors.New("algorithm service returned an invalid ranking reason")
			}
		}
	}
	return output, nil
}

func isTrustedPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "algorithm") || strings.HasSuffix(strings.ToLower(host), ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("backend-%d", time.Now().UnixNano())
}
