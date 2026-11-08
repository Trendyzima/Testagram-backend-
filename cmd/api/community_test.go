package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommunitySlugNormalizesDisplayNames(t *testing.T) {
	cases := map[string]string{
		"Kenya Tech Hub": "kenya-tech-hub",
		"  Art & Design  ": "art-design",
		"Community_One": "community_one",
	}
	for input, want := range cases {
		if got := communitySlug(input); got != want {
			t.Errorf("communitySlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCommunityRoutesRequireCanonicalDatabase(t *testing.T) {
	handler := (&service{}).routes()
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/communities", ""},
		{http.MethodGet, "/api/communities/00000000-0000-4000-8000-000000000001/posts", ""},
		{http.MethodGet, "/api/communities/00000000-0000-4000-8000-000000000001/chat", ""},
		{http.MethodGet, "/api/social/polls/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodGet, "/api/social/posts/00000000-0000-4000-8000-000000000001/poll", ""},
		{http.MethodGet, "/api/scheduled-posts", ""},
		{http.MethodPost, "/api/communities", `{"name":"Kenya Tech Hub","slug":"kenya-tech-hub"}`},
		{http.MethodPost, "/api/communities/00000000-0000-4000-8000-000000000001/join", `{}`},
		{http.MethodDelete, "/api/communities/00000000-0000-4000-8000-000000000001/leave", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Origin", "https://testagram.site")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
			}
		})
	}
}

func TestCommunityCreateInputRejectsMalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/communities", strings.NewReader(`{"name":`))
	req.Header.Set("Origin", "https://testagram.site")
	rec := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("database guard should fail closed before parsing unauthenticated writes: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
