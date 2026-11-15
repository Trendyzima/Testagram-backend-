package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// enqueueWatchdogImpulse sends only a coarse allowlisted event to the private
// Data Centre control plane. It carries no user ID, email, IP, post content,
// access token or session identifier. Delivery is best-effort: watchdog
// outages must never break login, feed reads, publishing or media access.
func validWatchdogEvent(eventType string) bool {
	switch eventType {
	case "login", "feed_open", "post_publish", "media_upload", "media_view", "federation_interaction":
		return true
	default:
		return false
	}
}

func watchdogBucketKey(eventType string, now time.Time) string {
	return "testagram:"+eventType+":"+now.UTC().Truncate(5*time.Minute).Format("20060102T1504")
}

func (s *service) enqueueWatchdogImpulse(eventType string) {
	switch eventType {
	case "login", "feed_open", "post_publish", "media_upload", "media_view", "federation_interaction":
	default:
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("VPS_WATCHDOG_INTERNAL_URL")), "/")
	token := strings.TrimSpace(os.Getenv("VPS_WATCHDOG_INTERNAL_TOKEN"))
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
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		log.Printf("activity watchdog idempotency key generation failed: %v", err)
		return
	}
	payload, err := json.Marshal(map[string]string{
		"idempotencyKey": hex.EncodeToString(keyBytes),
		"eventType":      eventType,
	})
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
