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

// Proxy forwards only Backend-selected private Data Centre video paths.
// Authenticated operations carry the already-validated native user token so
// the private service independently rechecks the same account and session.
// A small allowlist of public playback operations may omit that token.
func (c *Client) Proxy(w http.ResponseWriter, r *http.Request, rawPath, userToken string) error {
	if c == nil || c.baseURL == nil || c.http == nil {
		return errors.New("Data Centre video API is not configured")
	}
	parsed, err := url.ParseRequestURI(rawPath)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" ||
		!strings.HasPrefix(parsed.Path, "/v1/") || strings.Contains(parsed.Path, "\\") ||
		strings.ContainsAny(rawPath, "\\r\\n") {
		return errors.New("invalid Data Centre video API path")
	}
	for _, part := range strings.Split(parsed.Path, "/") {
		if part == "." || part == ".." {
			return errors.New("invalid Data Centre video API path")
		}
	}
	userToken = strings.TrimSpace(userToken)
	if userToken == "" && !publicVideoRoute(r.Method, parsed.Path) {
		return errors.New("validated native user token is required")
	}
	if parsed.RawQuery != "" && !strings.Contains(parsed.Path, "/hls/") {
		return errors.New("query parameters are not accepted for this video route")
	}
	target := *c.baseURL
	target.Path = parsed.Path
	target.RawPath = ""
	target.RawQuery = parsed.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return fmt.Errorf("build Data Centre video request: %w", err)
	}
	req.ContentLength = r.ContentLength
	if userToken != "" {
		if strings.ContainsAny(userToken, "\\r\\n") {
			return errors.New("invalid native user token")
		}
		req.Header.Set("Authorization", "Bearer "+userToken)
	}
	req.Header.Set("Accept", "application/json, application/vnd.apple.mpegurl, video/mp2t, */*")
	req.Header.Set("X-Testagram-API-Version", "1")
	for _, header := range []string{"Content-Type", "X-Request-ID", "X-Playback-Session"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			req.Header.Set(header, value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Data Centre video request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if r.Method != http.MethodGet || !strings.HasSuffix(parsed.Path, "/manifest") || resp.StatusCode != http.StatusTemporaryRedirect {
			return errors.New("Data Centre video API redirect is not accepted")
		}
		location, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || location.IsAbs() || location.Host != "" || location.User != nil ||
			location.Path != strings.TrimSuffix(parsed.Path, "/manifest")+"/hls/master.m3u8" ||
			location.RawQuery == "" || location.Fragment != "" {
			return errors.New("Data Centre video manifest returned an unsafe playback location")
		}
		query := location.Query()
		token := query.Get("token")
		if token == "" || len(query) != 1 {
			return errors.New("Data Centre video manifest returned an invalid playback token")
		}
		videoID := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/v1/videos/"), "/manifest")
		if videoID == "" || strings.Contains(videoID, "/") {
			return errors.New("invalid video manifest identifier")
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Location", "/api/video/uploads/"+videoID+"/hls/master.m3u8?token="+url.QueryEscape(token))
		w.WriteHeader(http.StatusTemporaryRedirect)
		return nil
	}
	for _, header := range []string{"Content-Type", "Cache-Control", "Retry-After", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if strings.Contains(parsed.Path, "/hls/") {
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			return fmt.Errorf("stream Data Centre video bytes: %w", err)
		}
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return errors.New("Data Centre video API returned an invalid or oversized response")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return nil
}

func publicVideoRoute(method, path string) bool {
	if method == http.MethodGet && path == "/v1/feed" {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "videos" && parts[2] != "" {
		switch parts[3] {
		case "manifest":
			return method == http.MethodGet
		case "view":
			return method == http.MethodPost
		}
	}
	return len(parts) == 5 && method == http.MethodGet &&
		parts[0] == "v1" && parts[1] == "videos" && parts[2] != "" &&
		parts[3] == "hls" && parts[4] != ""
}
