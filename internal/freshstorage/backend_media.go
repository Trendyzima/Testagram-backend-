package freshstorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var freshMediaObjectKeyPattern = regexp.MustCompile(`^users/([A-Za-z0-9._-]{1,128})/([a-f0-9]{32})$`)

type MediaDelivery struct {
	URL              string `json:"url"`
	Authorization    string `json:"authorization,omitempty"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
	Visibility       string `json:"visibility"`
}

type BackendMediaClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewBackendMediaClient(baseURL, token string, client *http.Client) (*BackendMediaClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" && token == "" {
		return nil, nil
	}
	if baseURL == "" || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Fresh media Backend URL and a 32-character internal token are required together")
	}
	if err := validateBackendAPIBaseURL(baseURL); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &BackendMediaClient{baseURL: baseURL, token: token, client: &copyClient}, nil
}

func (c *BackendMediaClient) Register(ctx context.Context, r Reservation) error {
	if c == nil {
		return nil
	}
	body := map[string]any{
		"viewer_id": r.OwnerID, "owner_id": r.OwnerID, "key": r.ObjectKey, "visibility": "private",
		"content_type": r.ContentType, "size_bytes": r.Bytes, "sha256": r.SHA256,
	}
	return c.post(ctx, "/internal/media/fresh/register", body, nil)
}

func (c *BackendMediaClient) Revoke(ctx context.Context, ownerID, key string) error {
	if c == nil {
		return nil
	}
	return c.post(ctx, "/internal/media/fresh/revoke", map[string]string{
		"owner_id": strings.TrimSpace(ownerID), "key": strings.TrimSpace(key),
	}, nil)
}

func (c *BackendMediaClient) Issue(ctx context.Context, viewerID, ownerID, key string) (MediaDelivery, error) {
	if c == nil {
		return MediaDelivery{}, errors.New("Fresh media Backend assertion bridge is not configured")
	}
	var result MediaDelivery
	if err := c.post(ctx, "/internal/media/fresh/cdn-download-url", map[string]string{
		"viewer_id": strings.TrimSpace(viewerID), "owner_id": strings.TrimSpace(ownerID), "key": strings.TrimSpace(key),
	}, &result); err != nil {
		return MediaDelivery{}, err
	}
	if !strings.HasPrefix(result.URL, "https://") || !strings.HasPrefix(result.Authorization, "Bearer v1.") ||
		result.ExpiresInSeconds < 1 || result.ExpiresInSeconds > 300 || result.Visibility != "private" {
		return MediaDelivery{}, errors.New("Backend returned an invalid private media authorization")
	}
	return result, nil
}

func (c *BackendMediaClient) post(ctx context.Context, path string, payload any, result any) error {
	if c == nil {
		return errors.New("Fresh media Backend bridge is not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-Fresh-Media-Token", c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("Fresh media Backend request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("read Fresh media Backend response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Fresh media Backend rejected request with HTTP %d", resp.StatusCode)
	}
	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return errors.New("Fresh media Backend returned an invalid response")
		}
	}
	return nil
}
