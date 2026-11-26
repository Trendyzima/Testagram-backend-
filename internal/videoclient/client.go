package videoclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 64 << 10
const maxHLSResponseBytes = 64 << 20

type Client struct {
	baseURL *url.URL
	http    *http.Client
}

func New(baseURL string, client *http.Client) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("Data Centre video API URL must be an absolute HTTP(S) origin")
	}
	if !privateServiceHost(u.Hostname()) {
		return nil, errors.New("Data Centre video API must resolve to a private service host")
	}
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
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
	return &Client{baseURL: u, http: client}, nil
}

func privateServiceHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || host == "video" || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// Proxy forwards only fixed Backend-selected /v1/video paths. It forwards the
// validated native user token so the private Data Centre service can recheck
// the account/session against its own PostgreSQL connection.
func (c *Client) Proxy(w http.ResponseWriter, r *http.Request, path, userToken string) error {
	if c == nil || c.baseURL == nil || c.http == nil {
		return errors.New("Data Centre video API is not configured")
	}
	if !strings.HasPrefix(path, "/v1/") || strings.Contains(path, "..") || strings.ContainsAny(path, "\r\n") {
		return errors.New("invalid Data Centre video API path")
	}
	userToken = strings.TrimSpace(userToken)
	if userToken == "" || strings.ContainsAny(userToken, "\r\n") {
		return errors.New("validated native user token is required")
	}
	target := *c.baseURL
	target.Path = path
	target.RawPath = ""
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return fmt.Errorf("build Data Centre video request: %w", err)
	}
	req.ContentLength = r.ContentLength
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	for _, header := range []string{"Range", "If-Range", "X-Playback-Session"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" { req.Header.Set(header, value) }
	}
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if requestID := strings.TrimSpace(r.Header.Get("X-Request-ID")); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Data Centre video request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if resp.StatusCode != http.StatusTemporaryRedirect { return errors.New("Data Centre video API redirect status is not accepted") }
		location, ok := publicHLSLocation(path, resp.Header.Get("Location"))
		if !ok { return errors.New("Data Centre video API returned an unsafe playback redirect") }
		w.Header().Set("Location", location)
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(resp.StatusCode)
		return nil
	}
	limit := int64(maxResponseBytes)
	if strings.Contains(path, "/hls/") { limit = maxHLSResponseBytes }
	if resp.ContentLength > limit { return errors.New("Data Centre video API returned an oversized response") }
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(header); value != "" { w.Header().Set(header, value) }
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if cacheControl := resp.Header.Get("Cache-Control"); cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, limit))
	return nil
}

func publicHLSLocation(requestPath, rawLocation string) (string, bool) {
	requested := strings.Split(strings.Trim(requestPath, "/"), "/")
	location, err := url.Parse(strings.TrimSpace(rawLocation))
	if err != nil || location.IsAbs() || location.Host != "" || location.User != nil || location.Fragment != "" { return "", false }
	query := location.Query()
	if len(query) != 1 || len(query["token"]) != 1 || query.Get("token") == "" || len(query.Get("token")) > 256 {
		return "", false
	}
	parts := strings.Split(strings.Trim(location.Path, "/"), "/")
	if len(requested) != 4 || requested[0] != "v1" || requested[1] != "videos" || requested[3] != "manifest" || len(parts) != 5 || parts[0] != "v1" || parts[1] != "videos" || parts[2] != requested[2] || parts[3] != "hls" || parts[4] == "" || strings.Contains(parts[4], "..") || strings.ContainsAny(parts[4], "/\\") { return "", false }
	for _, c := range parts[4] { if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') { return "", false } }
	return "/api/video/uploads/" + requested[2] + "/hls/" + parts[4] + "?" + location.RawQuery, true
}
