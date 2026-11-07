package datacentreclient

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

type Client struct {
	baseURL *url.URL
	token string
	http *http.Client
}

type UploadGrant struct {
	IntentID string `json:"intent_id"`
	UploadURL string `json:"upload_url"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxBytes int64 `json:"max_bytes"`
	Primary string `json:"primary"`
}

type Verification struct {
	Status string `json:"status"`
	SHA256 string `json:"sha256"`
}

type MediaStatus struct {
	Status string `json:"status"`
	ContentType string `json:"content_type"`
	SizeBytes int64 `json:"size_bytes"`
	SHA256 string `json:"sha256"`
	PrimaryVerifiedAt *time.Time `json:"primary_verified_at"`
	ReplicaVerifiedAt *time.Time `json:"replica_verified_at"`
}

type DownloadGrant struct {
	URL string `json:"url"`
	Provider string `json:"provider"`
}

type uploadRequest struct {
	OwnerID string `json:"owner_id"`
	Namespace string `json:"namespace"`
	Key string `json:"key"`
	ContentType string `json:"content_type"`
	SizeBytes int64 `json:"size_bytes"`
}

type verifyRequest struct {
	OwnerID string `json:"owner_id"`
	Namespace string `json:"namespace"`
	Key string `json:"key"`
	ContentType string `json:"content_type"`
	SizeBytes int64 `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

type downloadRequest struct {
	RequesterID string `json:"requester_id"`
	OwnerID string `json:"owner_id"`
	Namespace string `json:"namespace"`
	Key string `json:"key"`
}

type deleteRequest struct {
	RequesterID string `json:"requester_id"`
	OwnerID string `json:"owner_id"`
	Namespace string `json:"namespace"`
	Key string `json:"key"`
}

func New(baseURL, token string, client *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" && token == "" {
		return nil, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Data Centre media API URL must be an absolute HTTP(S) origin")
	}
	if u.Scheme == "http" && !isPrivateServiceHost(u.Hostname()) {
		return nil, errors.New("Data Centre media API must use HTTPS outside private networks")
	}
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Data Centre media service token must contain at least 32 single-line characters")
	}
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	} else {
		copyClient := *client
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		client = &copyClient
	}
	return &Client{baseURL: u, token: token, http: client}, nil
}

func isPrivateServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".internal") || host == "control-plane" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func (c *Client) CreateUploadURL(ctx context.Context, ownerID, namespace, key, contentType string, size int64) (UploadGrant, error) {
	var result UploadGrant
	err := c.post(ctx, "/internal/v1/media/upload-url", uploadRequest{
		OwnerID: ownerID, Namespace: namespace, Key: key, ContentType: contentType, SizeBytes: size,
	}, &result)
	if err != nil {
		return UploadGrant{}, err
	}
	if result.UploadURL == "" || result.MaxBytes != size || result.Primary != "backblaze_b2" {
		return UploadGrant{}, errors.New("Data Centre returned an invalid upload authorization")
	}
	return result, nil
}

func (c *Client) Verify(ctx context.Context, ownerID, namespace, key, contentType, sha256Hex string, size int64) (Verification, error) {
	var result Verification
	err := c.post(ctx, "/internal/v1/media/verify", verifyRequest{
		OwnerID: ownerID, Namespace: namespace, Key: key, ContentType: contentType,
		SizeBytes: size, SHA256: strings.ToLower(sha256Hex),
	}, &result)
	if err != nil {
		return Verification{}, err
	}
	if result.Status != "replication_pending" && result.Status != "protected" {
		return Verification{}, errors.New("Data Centre returned an invalid verification state")
	}
	return result, nil
}

func (c *Client) Status(ctx context.Context, ownerID, namespace, key string) (MediaStatus, error) {
	var result MediaStatus
	query := url.Values{}
	query.Set("owner_id", ownerID)
	query.Set("namespace", namespace)
	query.Set("key", key)
	err := c.get(ctx, "/internal/v1/media/status?"+query.Encode(), &result)
	if err != nil {
		return MediaStatus{}, err
	}
	return result, nil
}

func (c *Client) DownloadURL(ctx context.Context, requesterID, ownerID, namespace, key string) (DownloadGrant, error) {
	var result DownloadGrant
	err := c.post(ctx, "/internal/v1/media/download-url", downloadRequest{
		RequesterID: requesterID, OwnerID: ownerID, Namespace: namespace, Key: key,
	}, &result)
	if err != nil {
		return DownloadGrant{}, err
	}
	if result.URL == "" || (result.Provider != "cloudflare_r2" && result.Provider != "backblaze_b2") {
		return DownloadGrant{}, errors.New("Data Centre returned an invalid media delivery URL")
	}
	return result, nil
}

func (c *Client) Delete(ctx context.Context, requesterID, ownerID, namespace, key string) (string, error) {
	var result struct {
		Status string `json:"status"`
	}
	err := c.post(ctx, "/internal/v1/media/delete", deleteRequest{
		RequesterID: requesterID, OwnerID: ownerID, Namespace: namespace, Key: key,
	}, &result)
	if err != nil {
		return "", err
	}
	if result.Status != "deleted" && result.Status != "delete_pending" {
		return "", errors.New("Data Centre returned an invalid media deletion state")
	}
	return result.Status, nil
}

func (c *Client) post(ctx context.Context, path string, body any, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, path, encoded, result)
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	return c.request(ctx, http.MethodGet, path, nil, result)
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, result any) error {
	if c == nil || c.baseURL == nil || c.http == nil {
		return errors.New("Data Centre media API is not configured")
	}
	target := *c.baseURL
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" {
		return errors.New("invalid Data Centre media API path")
	}
	target.Path = strings.TrimRight(target.Path, "/") + relative.Path
	target.RawQuery = relative.RawQuery
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	req.Header.Set("X-Request-ID", requestID())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Data Centre media API request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return errors.New("Data Centre media API response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Data Centre media API returned HTTP %d", resp.StatusCode)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("Data Centre media API returned invalid JSON")
	}
	return nil
}

func requestID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err == nil {
		return hex.EncodeToString(id[:])
	}
	return fmt.Sprintf("dc-%d", time.Now().UnixNano())
}
