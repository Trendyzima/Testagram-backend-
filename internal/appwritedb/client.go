package appwritedb

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

const maxResponseBytes = 2 << 20

type Config struct {
	Endpoint   string
	ProjectID  string
	APIKey     string
	DatabaseID string
	Client     *http.Client
}

type Client struct {
	base       *url.URL
	projectID  string
	apiKey     string
	databaseID string
	http       *http.Client
}

type HTTPError struct{ StatusCode int }

func (e HTTPError) Error() string {
	return fmt.Sprintf("Appwrite Database returned HTTP %d", e.StatusCode)
}

type Document struct {
	ID          string         `json:"$id"`
	Collection  string         `json:"$collectionId"`
	Database    string         `json:"$databaseId"`
	Data        map[string]any `json:"-"`
	Permissions []string       `json:"$permissions,omitempty"`
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && isLocalHost(u.Hostname()))) {
		return nil, errors.New("Appwrite endpoint must be HTTPS (HTTP only on localhost) and contain no credentials, query or fragment")
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		return nil, errors.New("Appwrite endpoint path must end in /v1")
	}
	if !validID(cfg.ProjectID) || !validID(cfg.DatabaseID) || strings.TrimSpace(cfg.APIKey) == "" ||
		strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("Appwrite project, database or server API key is invalid")
	}
	hc := cfg.Client
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	copyClient := *hc
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: u, projectID: cfg.ProjectID, apiKey: cfg.APIKey, databaseID: cfg.DatabaseID, http: &copyClient}, nil
}

func (c *Client) CreateDocument(ctx context.Context, collectionID, documentID string, data map[string]any, permissions []string) (json.RawMessage, error) {
	if !validID(collectionID) || (documentID != "unique()" && !validID(documentID)) || data == nil {
		return nil, errors.New("invalid Appwrite collection, document ID or data")
	}
	body := map[string]any{"documentId": documentID, "data": data}
	if permissions != nil {
		body["permissions"] = permissions
	}
	return c.do(ctx, http.MethodPost, c.documentsPath(collectionID, ""), body)
}

func (c *Client) GetDocument(ctx context.Context, collectionID, documentID string) (json.RawMessage, error) {
	if !validID(collectionID) || !validID(documentID) {
		return nil, errors.New("invalid Appwrite collection or document ID")
	}
	return c.do(ctx, http.MethodGet, c.documentsPath(collectionID, documentID), nil)
}

func (c *Client) UpdateDocument(ctx context.Context, collectionID, documentID string, data map[string]any) (json.RawMessage, error) {
	if !validID(collectionID) || !validID(documentID) || data == nil {
		return nil, errors.New("invalid Appwrite collection, document ID or data")
	}
	return c.do(ctx, http.MethodPatch, c.documentsPath(collectionID, documentID), map[string]any{"data": data})
}

func (c *Client) DeleteDocument(ctx context.Context, collectionID, documentID string) error {
	if !validID(collectionID) || !validID(documentID) {
		return errors.New("invalid Appwrite collection or document ID")
	}
	_, err := c.do(ctx, http.MethodDelete, c.documentsPath(collectionID, documentID), nil)
	return err
}

func (c *Client) documentsPath(collectionID, documentID string) string {
	p := c.base.Path + "/databases/" + url.PathEscape(c.databaseID) + "/collections/" + url.PathEscape(collectionID) + "/documents"
	if documentID != "" {
		p += "/" + url.PathEscape(documentID)
	}
	return p
}

func (c *Client) do(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode Appwrite request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	u := *c.base
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, errors.New("construct Appwrite database request")
	}
	req.Header.Set("X-Appwrite-Project", c.projectID)
	req.Header.Set("X-Appwrite-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Appwrite Database request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, errors.New("read Appwrite Database response")
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("Appwrite Database response exceeded limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, HTTPError{StatusCode: resp.StatusCode}
	}
	if method == http.MethodDelete {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("Appwrite Database returned invalid JSON")
	}
	return json.RawMessage(raw), nil
}

func validID(s string) bool {
	if s == "" || len(s) > 128 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func isLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
