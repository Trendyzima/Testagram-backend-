package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
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
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("invalid hashtag pattern %q: %v", pattern, err)
	}
	for _, content := range []string{"#kenya2026", "hello #kenya2026 world", "(#kenya2026)", "x #kenya2026!"} {
		if !matcher.MatchString(content) {
			t.Errorf("hashtag pattern did not match %q", content)
		}
	}
	for _, content := range []string{"kenya2026", "#kenya20260", "word#kenya2026"} {
		if matcher.MatchString(content) {
			t.Errorf("hashtag pattern unexpectedly matched %q", content)
		}
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

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/hashtags/testagram/follow"},
		{http.MethodPost, "/api/hashtags/testagram/follow"},
		{http.MethodDelete, "/api/hashtags/testagram/follow"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "SOCIAL_DATABASE_UNAVAILABLE") {
			t.Errorf("%s %s without DB: status=%d body=%s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
}
