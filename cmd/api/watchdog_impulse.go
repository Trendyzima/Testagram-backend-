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

// emitWatchdogImpulse is best-effort and never participates in user auth or
// product-data transactions. The caller invokes it only after the relevant
// existing authentication/authorization or product write has succeeded.
// No user identifiers, session credentials or post content leave Backend.
func emitWatchdogImpulse(ctx context.Context, event string) {
	switch event {
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
		log.Printf("watchdog impulse skipped: invalid private control-plane URL")
		return
	}
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		log.Printf("watchdog impulse key generation failed: %v", err)
		return
	}
	payload, err := json.Marshal(map[string]string{
		"idempotencyKey": hex.EncodeToString(keyBytes),
		"eventType":      event,
	})
	if err != nil {
		return
	}
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
		log.Printf("watchdog impulse delivery failed event=%s: %v", event, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		log.Printf("watchdog impulse rejected event=%s status=%d", event, resp.StatusCode)
	}
}
