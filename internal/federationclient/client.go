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


func validATProtoDID(did string) bool {
	did = strings.TrimSpace(did)
	if len(did) < 12 || len(did) > 512 || strings.ContainsAny(did, " \t\r\n") {
		return false
	}
	if strings.HasPrefix(did, "did:plc:") {
		identifier := strings.TrimPrefix(did, "did:plc:")
		if len(identifier) != 24 { return false }
		for _, char := range identifier {
			if !(char >= 'a' && char <= 'z') && !(char >= '2' && char <= '7') { return false }
		}
		return true
	}
	if strings.HasPrefix(did, "did:web:") {
		identifier := strings.TrimPrefix(did, "did:web:")
		return identifier != "" && !strings.ContainsAny(identifier, " /?#@")
	}
	return false
}

type RemoteObjectsRequest struct {
    ActorURIs []string `json:"actor_uris"`
    Limit int `json:"limit"`
    Cursor string `json:"cursor,omitempty"`
}
type RemoteObjectCandidate struct {
    ObjectURI string `json:"object_uri"`
    ActorURI string `json:"actor_uri"`
    ObjectType string `json:"object_type"`
    Content string `json:"content"`
    PublishedAt *time.Time `json:"published_at,omitempty"`
    Visibility string `json:"visibility"`
    CreatedAt time.Time `json:"created_at"`
}
type RemoteObjectsPage struct {
    Items []RemoteObjectCandidate `json:"items"`
    NextCursor string `json:"next_cursor"`
    HasMore bool `json:"has_more"`
}
type ATProtoFeedRequest struct {
    ActorDIDs []string `json:"actor_dids"`
    LimitPerActor int `json:"limit_per_actor"`
}
type ATProtoProfile struct {
    DID string `json:"did"`
    Handle string `json:"handle"`
    DisplayName string `json:"displayName,omitempty"`
}
type ATProtoPost struct {
    URI string `json:"uri"`
    CID string `json:"cid"`
    Text string `json:"text"`
    CreatedAt string `json:"createdAt,omitempty"`
    IndexedAt string `json:"indexedAt,omitempty"`
    Author ATProtoProfile `json:"author"`
    ReplyCount int64 `json:"replyCount"`
    RepostCount int64 `json:"repostCount"`
    LikeCount int64 `json:"likeCount"`
    QuoteCount int64 `json:"quoteCount"`
    Labels []string `json:"labels,omitempty"`
}
type ATProtoActorFeed struct {
    DID string `json:"did"`
    Handle string `json:"handle"`
    Cursor string `json:"cursor,omitempty"`
    Posts []ATProtoPost `json:"posts"`
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
	if timeout <= 0 || timeout > 20*time.Second {
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

type remoteObjectsEnvelope struct {
    OK bool `json:"ok"`
    Data struct {
        Items []RemoteObjectCandidate `json:"items"`
        NextCursor string `json:"next_cursor"`
        HasMore bool `json:"has_more"`
    } `json:"data"`
}
type atprotoFeedsEnvelope struct {
    OK bool `json:"ok"`
    Data struct {
        Source string `json:"source"`
        Actors []ATProtoActorFeed `json:"actors"`
    } `json:"data"`
}

// FetchRemoteObjects requests projections only for actor URIs that Backend
// has already authorized for the current viewer. Federation never authorizes
// the viewer and never returns followers-only/direct objects through this API.
func (c *Client) FetchRemoteObjects(ctx context.Context, input RemoteObjectsRequest) (RemoteObjectsPage, error) {
    if c == nil { return RemoteObjectsPage{}, errors.New("Federation client is not configured") }
    if len(input.ActorURIs) == 0 || len(input.ActorURIs) > 100 || input.Limit < 1 || input.Limit > 100 || len(input.Cursor) > 2048 {
        return RemoteObjectsPage{}, errors.New("invalid Backend-authorized remote actor query")
    }
    seen := make(map[string]struct{}, len(input.ActorURIs))
    for i, uri := range input.ActorURIs {
        uri = strings.TrimSpace(uri)
        input.ActorURIs[i] = uri
        u, err := url.Parse(uri)
        if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
            return RemoteObjectsPage{}, errors.New("remote actor allowlist contains an invalid HTTPS URI")
        }
        if _, ok := seen[uri]; ok { return RemoteObjectsPage{}, errors.New("remote actor allowlist contains duplicates") }
        seen[uri] = struct{}{}
    }
    var result remoteObjectsEnvelope
    if err := c.postInternalJSON(ctx, "/internal/v1/remote-objects", input, &result); err != nil { return RemoteObjectsPage{}, err }
    if !result.OK || len(result.Data.Items) > input.Limit || result.Data.HasMore != (result.Data.NextCursor != "") {
        return RemoteObjectsPage{}, errors.New("Federation returned an invalid remote object page")
    }
    for _, item := range result.Data.Items {
        objectURL, parseErr := url.Parse(item.ObjectURI)
        if parseErr != nil || objectURL.Scheme != "https" || objectURL.Host == "" || objectURL.User != nil ||
            objectURL.RawQuery != "" || objectURL.Fragment != "" ||
            (item.ObjectType != "Note" && item.ObjectType != "Article") ||
            item.ActorURI == "" || len(item.Content) == 0 || len(item.Content) > 20000 ||
            (item.Visibility != "public" && item.Visibility != "unlisted") {
            return RemoteObjectsPage{}, errors.New("Federation returned an invalid or non-public remote object")
        }
        if _, ok := seen[item.ActorURI]; !ok { return RemoteObjectsPage{}, errors.New("Federation returned an actor outside the authorized allowlist") }
    }
    return RemoteObjectsPage{Items: result.Data.Items, NextCursor: result.Data.NextCursor, HasMore: result.Data.HasMore}, nil
}

// FetchATProtoFeeds accepts only Backend-selected DIDs, then verifies every
// returned author DID and AT URI before passing the projections to callers.
func (c *Client) FetchATProtoFeeds(ctx context.Context, input ATProtoFeedRequest) ([]ATProtoActorFeed, error) {
    if c == nil { return nil, errors.New("Federation client is not configured") }
    if len(input.ActorDIDs) == 0 || len(input.ActorDIDs) > 20 || input.LimitPerActor < 1 || input.LimitPerActor > 25 {
        return nil, errors.New("invalid Backend-authorized AT Protocol feed request")
    }
    seen := make(map[string]struct{}, len(input.ActorDIDs))
    for _, did := range input.ActorDIDs {
        if !validATProtoDID(did) {
            return nil, errors.New("AT Protocol actor list contains an invalid DID")
        }
        if _, ok := seen[did]; ok { return nil, errors.New("AT Protocol actor list contains duplicate DIDs") }
        seen[did] = struct{}{}
    }
    var result atprotoFeedsEnvelope
    if err := c.postInternalJSON(ctx, "/internal/v1/atproto/feed", input, &result); err != nil { return nil, err }
    if !result.OK || result.Data.Source != "bluesky-atproto" || len(result.Data.Actors) != len(seen) {
        return nil, errors.New("Federation returned an invalid AT Protocol feed envelope")
    }
    returned := make(map[string]struct{}, len(result.Data.Actors))
    for _, actor := range result.Data.Actors {
        if _, ok := seen[actor.DID]; !ok || actor.Handle == "" { return nil, errors.New("Federation returned an unauthorized AT Protocol actor") }
        if _, ok := returned[actor.DID]; ok { return nil, errors.New("Federation returned duplicate AT Protocol actors") }
        returned[actor.DID] = struct{}{}
        if len(actor.Posts) > input.LimitPerActor || len(actor.Cursor) > 512 {
            return nil, errors.New("Federation returned an oversized AT Protocol feed page")
        }
        for _, post := range actor.Posts {
            if post.Author.DID != actor.DID || !strings.HasPrefix(post.URI, "at://"+actor.DID+"/app.bsky.feed.post/") ||
                post.CID == "" || len(post.CID) > 256 || len(post.Text) > 20000 {
                return nil, errors.New("Federation returned an AT Protocol post with mismatched provenance")
            }
        }
    }
    return result.Data.Actors, nil
}

func (c *Client) postInternalJSON(ctx context.Context, path string, input any, output any) error {
    body, err := json.Marshal(input)
    if err != nil { return fmt.Errorf("encode Federation request: %w", err) }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
    if err != nil { return err }
    req.Header.Set("Authorization", "Bearer "+c.token)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")
    req.Header.Set("X-Testagram-API-Version", "1")
    req.Header.Set("X-Request-ID", newRequestID())
    resp, err := c.http.Do(req)
    if err != nil { return fmt.Errorf("call private Federation API: %w", err) }
    defer resp.Body.Close()
    raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
    if err != nil || len(raw) > 1<<20 { return errors.New("Federation response is unreadable or too large") }
    if resp.StatusCode != http.StatusOK { return fmt.Errorf("private Federation API returned HTTP %d", resp.StatusCode) }
    if err := json.Unmarshal(raw, output); err != nil { return errors.New("Federation returned invalid JSON") }
    return nil
}
