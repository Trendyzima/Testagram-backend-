package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func validNotificationStreamOrigin(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", errors.New("notification stream origin is not configured")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("notification stream origin must be an absolute HTTP(S) origin")
	}
	if parsed.Scheme != "https" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		privateHost := host == "localhost" || host == "notification" ||
			host == "notification-service" || strings.HasSuffix(host, ".internal") ||
			(ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()))
		if parsed.Scheme != "http" || !privateHost {
			return "", errors.New("notification stream must use HTTPS or a private internal HTTP origin")
		}
	}
	return raw, nil
}

type notificationFlushWriter struct {
	writer  http.ResponseWriter
	flusher http.Flusher
}

func (w notificationFlushWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.flusher.Flush()
	return n, err
}

// socialNotificationStream authenticates at Backend first, then forwards the
// same short-lived native user bearer over the private service network. The
// Data Centre resolves the user again and streams only that user's canonical
// testagram_local.notifications rows. The client never receives the private
// Data Centre address.
func (s *service) socialNotificationStream(w http.ResponseWriter, r *http.Request) {
	_, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	origin, err := validNotificationStreamOrigin(s.notificationStreamURL)
	if err != nil || s.notificationStreamHTTP == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATION_STREAM_UNAVAILABLE", "Real-time notifications are temporarily unavailable", requestID))
		return
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization == "" || len(authorization) > 8192 {
		writeJSON(w, http.StatusUnauthorized, errorBody("UNAUTHORIZED", "A valid Testagram session is required", requestID))
		return
	}
	lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if len(lastEventID) > 512 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_NOTIFICATION_CURSOR", "Notification cursor is invalid", requestID))
		return
	}
	target := origin + "/v2/stream"
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATION_STREAM_UNAVAILABLE", "Real-time notifications are temporarily unavailable", requestID))
		return
	}
	upstreamRequest.Header.Set("Authorization", authorization)
	upstreamRequest.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		upstreamRequest.Header.Set("Last-Event-ID", lastEventID)
	}
	upstreamResponse, err := s.notificationStreamHTTP.Do(upstreamRequest)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("NOTIFICATION_STREAM_UPSTREAM_FAILED", "Real-time notifications could not be reached", requestID))
		return
	}
	defer upstreamResponse.Body.Close()
	if upstreamResponse.StatusCode != http.StatusOK ||
		!strings.HasPrefix(strings.ToLower(upstreamResponse.Header.Get("Content-Type")), "text/event-stream") {
		_, _ = io.Copy(io.Discard, io.LimitReader(upstreamResponse.Body, 4096))
		writeJSON(w, http.StatusBadGateway, errorBody("NOTIFICATION_STREAM_UPSTREAM_INVALID", "Real-time notification service returned an invalid response", requestID))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody("STREAMING_UNSUPPORTED", "This connection cannot stream notifications", requestID))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	_, _ = io.Copy(notificationFlushWriter{writer: w, flusher: flusher}, upstreamResponse.Body)
}

