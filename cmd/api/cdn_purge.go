package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cdnPurger invalidates a single public media cache key through the private
// Backend-to-CDN network path. It never follows redirects with the purge token.
type cdnPurger struct {
	endpoint *url.URL
	token    string
	client   *http.Client
}

func newCDNPurger(endpoint, token string) (*cdnPurger, error) {
	endpoint = strings.TrimSpace(endpoint)
	token = strings.TrimSpace(token)
	if endpoint == "" && token == "" {
		return nil, nil
	}
	if endpoint == "" || token == "" {
		return nil, errors.New("CDN_PURGE_URL and CDN_PURGE_TOKEN must be configured together")
	}
	if len(token) < 32 {
		return nil, errors.New("CDN_PURGE_TOKEN must contain at least 32 bytes")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("CDN_PURGE_URL is invalid")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Path != "/api/cache/purge" {
		return nil, errors.New("CDN_PURGE_URL must be an absolute HTTP(S) URL to /api/cache/purge without userinfo or fragment")
	}
	return &cdnPurger{
		endpoint: parsed,
		token:    token,
		client: &http.Client{
			Timeout: 4 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Purge treats HTTP 404 as success: an object that is not cached is already
// invalidated. The durable media deletion job retries every other failure.
func (p *cdnPurger) Purge(ctx context.Context, objectKey string) error {
	if p == nil {
		return nil
	}
	objectKey = strings.TrimSpace(objectKey)
	if objectKey == "" || strings.ContainsAny(objectKey, "\r\n") {
		return errors.New("CDN purge object key is invalid")
	}
	target := *p.endpoint
	query := target.Query()
	query.Set("path", objectKey)
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), nil)
	if err != nil {
		return errors.New("could not create CDN purge request")
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("CDN purge request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("CDN purge returned HTTP %d", resp.StatusCode)
}
