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
	"regexp"
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

type TopicCandidate struct {
	Tag              string    `json:"tag"`
	PostsCount       int64     `json:"postsCount"`
	LastUsedAt       time.Time `json:"lastUsedAt"`
	InterestAffinity float64   `json:"interestAffinity"`
}

type TopicRequest struct {
	Now        time.Time        `json:"now"`
	Candidates []TopicCandidate `json:"candidates"`
	Limit      int              `json:"limit"`
}

type RankedTopic struct {
	Tag        string   `json:"tag"`
	PostsCount int64    `json:"postsCount"`
	Score      float64  `json:"score"`
	Reasons    []string `json:"reasons"`
}

type TopicsResponse struct {
	Items []RankedTopic `json:"items"`
}

var topicTagPattern = regexp.MustCompile("^[\\p{L}\\p{N}_]{1,64}$")

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
	allowed := make(map[string]struct{}, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if strings.TrimSpace(candidate.ID) == "" { return Response{}, errors.New("algorithm request contains an empty candidate ID") }
		if _, duplicate := allowed[candidate.ID]; duplicate { return Response{}, errors.New("algorithm request contains a duplicate candidate ID") }
		allowed[candidate.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(output.Items))
	for _, item := range output.Items {
		if _, supplied := allowed[item.ID]; !supplied { return Response{}, errors.New("algorithm service returned an ID outside the supplied candidate set") }
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


// RankTopics orders a bounded set of public hashtag candidates. Backend remains
// responsible for candidate discovery, visibility, blocks, mutes and final output.
func (c *Client) RankTopics(ctx context.Context, input TopicRequest) (TopicsResponse, error) {
	if c == nil {
		return TopicsResponse{}, errors.New("algorithm client is not configured")
	}
	if len(input.Candidates) == 0 || len(input.Candidates) > 200 {
		return TopicsResponse{}, errors.New("algorithm topic candidate count must be between 1 and 200")
	}
	if input.Limit < 1 || input.Limit > 50 {
		return TopicsResponse{}, errors.New("algorithm topic result limit must be between 1 and 50")
	}
	input.Candidates = append([]TopicCandidate(nil), input.Candidates...)
	allowed := make(map[string]int64, len(input.Candidates))
	for i := range input.Candidates {
		candidate := &input.Candidates[i]
		candidate.Tag = strings.ToLower(strings.TrimSpace(candidate.Tag))
		if !topicTagPattern.MatchString(candidate.Tag) || candidate.PostsCount < 1 ||
			candidate.PostsCount > 1_000_000_000 || candidate.LastUsedAt.IsZero() ||
			math.IsNaN(candidate.InterestAffinity) || math.IsInf(candidate.InterestAffinity, 0) ||
			candidate.InterestAffinity < 0 || candidate.InterestAffinity > 1 {
			return TopicsResponse{}, errors.New("algorithm request contains an invalid topic candidate")
		}
		if _, duplicate := allowed[candidate.Tag]; duplicate {
			return TopicsResponse{}, errors.New("algorithm request contains a duplicate topic tag")
		}
		allowed[candidate.Tag] = candidate.PostsCount
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return TopicsResponse{}, fmt.Errorf("encode topic ranking request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/topics/rank", bytes.NewReader(payload))
	if err != nil {
		return TopicsResponse{}, fmt.Errorf("create topic ranking request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	req.Header.Set("X-Request-ID", newRequestID())
	resp, err := c.http.Do(req)
	if err != nil {
		return TopicsResponse{}, fmt.Errorf("algorithm topic ranking request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return TopicsResponse{}, fmt.Errorf("algorithm topic ranker returned HTTP %d", resp.StatusCode)
	}
	var output TopicsResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<10))
	if err := decoder.Decode(&output); err != nil {
		return TopicsResponse{}, fmt.Errorf("decode topic ranking response: %w", err)
	}
	if len(output.Items) > input.Limit {
		return TopicsResponse{}, errors.New("algorithm topic ranker exceeded requested result limit")
	}
	seen := make(map[string]struct{}, len(output.Items))
	for _, item := range output.Items {
		count, supplied := allowed[item.Tag]
		if !supplied || item.PostsCount != count || math.IsNaN(item.Score) ||
			math.IsInf(item.Score, 0) || item.Score < 0 || item.Score > 1 {
			return TopicsResponse{}, errors.New("algorithm topic ranker returned an invalid or unsupplied topic")
		}
		if _, duplicate := seen[item.Tag]; duplicate {
			return TopicsResponse{}, errors.New("algorithm topic ranker returned a duplicate topic")
		}
		seen[item.Tag] = struct{}{}
		if len(item.Reasons) > 3 {
			return TopicsResponse{}, errors.New("algorithm topic ranker returned too many reasons")
		}
		for _, reason := range item.Reasons {
			switch reason {
			case "recent", "high-public-volume", "matches-viewer-interests":
			default:
				return TopicsResponse{}, errors.New("algorithm topic ranker returned an unknown reason")
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
