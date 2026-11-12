package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeHashtag(t *testing.T) {
	tag, pattern, ok := normalizeHashtag("  #Kenya2026  ")
	if !ok || tag != "kenya2026" {
		t.Fatalf("normalizeHashtag returned (%q, %q, %v)", tag, pattern, ok)
	}
	if !strings.Contains(pattern, "#kenya2026") {
		t.Fatalf("pattern does not identify the normalized tag: %q", pattern)
	}
	for _, invalid := range []string{"", "#", "bad tag", "tag/name", "tag!", strings.Repeat("a", 65)} {
		if _, _, valid := normalizeHashtag(invalid); valid {
			t.Errorf("normalizeHashtag(%q) unexpectedly accepted input", invalid)
		}
	}
}

func TestHashtagEndpointsFailClosedWithoutDatabase(t *testing.T) {
	handler := (&service{}).routes()

	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/hashtags/not-valid!", nil))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "INVALID_HASHTAG") {
		t.Fatalf("invalid hashtag: status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	timeline := httptest.NewRecorder()
	handler.ServeHTTP(timeline, httptest.NewRequest(http.MethodGet, "/api/hashtags/testagram", nil))
	if timeline.Code != http.StatusServiceUnavailable || !strings.Contains(timeline.Body.String(), "SOCIAL_DATABASE_UNAVAILABLE") {
		t.Fatalf("hashtag timeline without DB: status=%d body=%s", timeline.Code, timeline.Body.String())
	}

	trending := httptest.NewRecorder()
	handler.ServeHTTP(trending, httptest.NewRequest(http.MethodGet, "/api/hashtags/trending", nil))
	if trending.Code != http.StatusServiceUnavailable || !strings.Contains(trending.Body.String(), "SOCIAL_DATABASE_UNAVAILABLE") {
		t.Fatalf("trending without DB: status=%d body=%s", trending.Code, trending.Body.String())
	}
}
