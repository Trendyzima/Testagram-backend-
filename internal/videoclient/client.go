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
const maxVideoSegmentBytes = 256 << 20

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
	if strings.ContainsAny(userToken, "\r\n") {
		return errors.New("invalid native user token")
	}
	target := *c.baseURL
	target.Path = path
	target.RawPath = ""
	if strings.Contains(path, "/hls/") {
		target.RawQuery = r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return fmt.Errorf("build Data Centre video request: %w", err)
	}
	req.ContentLength = r.ContentLength
	if userToken != "" {
		req.Header.Set("Authorization", "Bearer "+userToken)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Testagram-API-Version", "1")
	for _, header := range []string{"Range", "If-Range"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			req.Header.Set(header, value)
		}
	}
	if contentType := strings.TrimSpace(r.Header.Get("Content-Type")); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if requestID := strings.TrimSpace(r.Header.Get("X-Request-ID")); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	if playbackSession := strings.TrimSpace(r.Header.Get("X-Playback-Session")); playbackSession != "" {
		req.Header.Set("X-Playback-Session", playbackSession)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Data Centre video request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTemporaryRedirect && strings.HasSuffix(path, "/manifest") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/videos/"), "/manifest")
		location, parseErr := url.Parse(resp.Header.Get("Location"))
		if parseErr != nil || location.IsAbs() || location.Host != "" || location.Fragment != "" ||
			location.Path != "/v1/videos/"+id+"/hls/master.m3u8" ||
			location.Query().Get("token") == "" || len(location.Query()) != 1 {
			return errors.New("Data Centre video manifest redirect is not an approved signed HLS location")
		}
		w.Header().Set("Location", "/api/video/uploads/"+id+"/hls/master.m3u8?token="+url.QueryEscape(location.Query().Get("token")))
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return errors.New("Data Centre video API redirects are not accepted")
	}
	if strings.Contains(path, "/hls/") {
		if resp.ContentLength > maxVideoSegmentBytes {
			return errors.New("Data Centre video segment exceeds the 256 MiB limit")
		}
		for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
			if value := resp.Header.Get(header); value != "" {
				w.Header().Set(header, value)
			}
		}
		if contentType := resp.Header.Get("Content-Type"); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if cacheControl := resp.Header.Get("Cache-Control"); cacheControl != "" {
			w.Header().Set("Cache-Control", cacheControl)
		} else if strings.HasSuffix(path, ".m3u8") {
			w.Header().Set("Cache-Control", "private, no-store")
		} else {
			w.Header().Set("Cache-Control", "private, max-age=300")
		}
		w.WriteHeader(resp.StatusCode)
		_, copyErr := io.Copy(w, io.LimitReader(resp.Body, maxVideoSegmentBytes+1))
		return copyErr
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return errors.New("Data Centre video API returned an invalid or oversized response")
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
	_, _ = w.Write(body)
	return nil
}
