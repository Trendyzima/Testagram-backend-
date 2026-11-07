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
	"strconv"
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

type HTTPError struct {
	StatusCode int
	Type       string
	Message    string
}

func (e HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Appwrite Database returned HTTP %d (%s): %s", e.StatusCode, e.Type, e.Message)
	}
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

func (c *Client) GetDocumentInTransaction(ctx context.Context, collectionID, documentID, transactionID string) (json.RawMessage, error) {
	if !validID(transactionID) || !validID(collectionID) || !validID(documentID) {
		return nil, errors.New("invalid Appwrite transactional document read")
	}
	return c.doQuery(ctx, http.MethodGet, c.documentsPath(collectionID, documentID), map[string]string{"transactionId": transactionID}, nil)
}

func (c *Client) ListDocuments(ctx context.Context, collectionID, query string, limit int) ([]json.RawMessage, error) {
	docs, _, err := c.ListDocumentsPage(ctx, collectionID, query, limit, 0)
	return docs, err
}

func (c *Client) ListDocumentsPage(ctx context.Context, collectionID, query string, limit, offset int) ([]json.RawMessage, int, error) {
	if !validID(collectionID) || strings.TrimSpace(query) == "" || limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, errors.New("invalid Appwrite document list query")
	}
	raw, err := c.doQuery(ctx, http.MethodGet, c.documentsPath(collectionID, ""), map[string]string{"queries[]": query, "limit": strconv.Itoa(limit), "offset": strconv.Itoa(offset)}, nil)
	if err != nil {
		return nil, 0, err
	}
	var result struct {
		Documents []json.RawMessage `json:"documents"`
		Total     int               `json:"total"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, 0, errors.New("invalid Appwrite document list response")
	}
	return result.Documents, result.Total, nil
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

func (c *Client) CreateTransaction(ctx context.Context, ttlSeconds int) (string, error) {
	if ttlSeconds < 60 || ttlSeconds > 300 {
		return "", errors.New("Appwrite transaction TTL must be between 60 and 300 seconds")
	}
	raw, err := c.do(ctx, http.MethodPost, c.base.Path+"/databases/transactions", map[string]any{"ttl": ttlSeconds})
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !validID(result.ID) {
		return "", errors.New("Appwrite returned an invalid transaction ID")
	}
	return result.ID, nil
}

func (c *Client) UpdateTransaction(ctx context.Context, transactionID string, commit bool) error {
	if !validID(transactionID) {
		return errors.New("invalid Appwrite transaction ID")
	}
	_, err := c.do(ctx, http.MethodPatch, c.base.Path+"/databases/transactions/"+url.PathEscape(transactionID), map[string]any{"commit": commit, "rollback": !commit})
	return err
}

func (c *Client) CreateDocumentInTransaction(ctx context.Context, collectionID, documentID string, data map[string]any, transactionID string) error {
	if !validID(transactionID) || !validID(collectionID) || !validID(documentID) || data == nil {
		return errors.New("invalid Appwrite transactional document")
	}
	_, err := c.doQuery(ctx, http.MethodPost, c.documentsPath(collectionID, ""), map[string]string{"transactionId": transactionID}, map[string]any{"documentId": documentID, "data": data})
	return err
}

func (c *Client) UpdateDocumentInTransaction(ctx context.Context, collectionID, documentID string, data map[string]any, transactionID string) error {
	if !validID(transactionID) || !validID(collectionID) || !validID(documentID) || data == nil {
		return errors.New("invalid Appwrite transactional document update")
	}
	_, err := c.doQuery(ctx, http.MethodPatch, c.documentsPath(collectionID, documentID), map[string]string{"transactionId": transactionID}, map[string]any{"data": data})
	return err
}

func (c *Client) IncrementDocumentAttribute(ctx context.Context, collectionID, documentID, attribute string, value, max int64, transactionID string) error {
	return c.numericDocumentAttribute(ctx, "increment", collectionID, documentID, attribute, value, max, transactionID)
}

func (c *Client) DecrementDocumentAttribute(ctx context.Context, collectionID, documentID, attribute string, value, min int64, transactionID string) error {
	return c.numericDocumentAttribute(ctx, "decrement", collectionID, documentID, attribute, value, min, transactionID)
}

func (c *Client) numericDocumentAttribute(ctx context.Context, operation, collectionID, documentID, attribute string, value, bound int64, transactionID string) error {
	if !validID(transactionID) || !validID(collectionID) || !validID(documentID) || !validID(attribute) || value < 1 || bound < 0 || (operation != "increment" && operation != "decrement") {
		return errors.New("invalid Appwrite atomic numeric operation")
	}
	body := map[string]any{"value": value}
	if operation == "increment" {
		body["max"] = bound
	} else {
		body["min"] = bound
	}
	path := c.documentsPath(collectionID, documentID) + "/" + url.PathEscape(attribute) + "/" + operation
	_, err := c.doQuery(ctx, http.MethodPatch, path, map[string]string{"transactionId": transactionID}, body)
	return err
}

func (c *Client) do(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	return c.doQuery(ctx, method, path, nil, payload)
}

func (c *Client) doQuery(ctx context.Context, method, path string, query map[string]string, payload any) (json.RawMessage, error) {
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
	q := u.Query()
	for key, value := range query {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
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
		var apiError struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &apiError)
		return nil, HTTPError{StatusCode: resp.StatusCode, Type: apiError.Type, Message: apiError.Message}
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
