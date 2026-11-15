package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func validWatchdogEvent(eventType string) bool {
	switch eventType {
	case "login", "feed_open", "post_publish", "media_upload", "media_view", "federation_interaction":
		return true
	default:
		return false
	}
}

// One platform-wide key per event type and five-minute bucket avoids storing
// user identifiers and prevents every feed refresh from generating a queue row.
func watchdogBucketKey(eventType string, at time.Time) string {
	return "testagram:" + eventType + ":" + at.UTC().Truncate(5*time.Minute).Format("20060102T1504")
}

func (s *service) enqueueWatchdogImpulse(eventType string) {
	if !validWatchdogEvent(eventType) {
		return
	}
	// Activity-driven activation is best-effort and must not add control-plane
	// latency to login, feed reads, publishing, or media authorization.
	go s.deliverWatchdogImpulse(eventType)
}

func (s *service) deliverWatchdogImpulse(eventType string) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("VPS_WATCHDOG_INTERNAL_URL")), "/")
	token := strings.TrimSpace(os.Getenv("VPS_WATCHDOG_INGRESS_TOKEN"))
	if base == "" || token == "" {
		return
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		log.Printf("activity watchdog skipped: invalid private control-plane URL")
		return
	}
	if parsed.Scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "control-plane" && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			log.Printf("activity watchdog skipped: plaintext control-plane URL must be loopback/private service DNS")
			return
		}
	}
	key := watchdogBucketKey(eventType, time.Now())
	payload, err := json.Marshal(map[string]string{"idempotencyKey": key, "eventType": eventType})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/watchdog/impulses", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{
		Timeout: 1500 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("activity watchdog delivery failed event=%s: %v", eventType, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		log.Printf("activity watchdog rejected event=%s status=%d", eventType, resp.StatusCode)
	}
}
