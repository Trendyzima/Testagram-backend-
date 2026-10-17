package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const attachUserID = "11111111-1111-4111-8111-111111111111"
const attachPostID = "22222222-2222-4222-8222-222222222222"
const attachMediaID = "33333333-3333-4333-8333-333333333333"

func TestMediaAttachRequiresOwnedUploadedMediaAndPost(t *testing.T) {
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "test-publishable-key")
	t.Setenv("SUPABASE_SERVICE_ROLE_KEY", "server-only-service-role")
	var patchCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/v1/user":
			if r.Header.Get("Authorization") != "Bearer user-session-token" || r.Header.Get("apikey") != "test-publishable-key" {
				t.Errorf("unexpected auth headers")
			}
			_, _ = w.Write([]byte("{\"id\":\"" + attachUserID + "\"}"))
		case r.URL.Path == "/rest/v1/posts" && r.Method == http.MethodGet:
			if r.Header.Get("Authorization") != "Bearer server-only-service-role" {
				t.Errorf("service role key was not used server-side")
			}
			_, _ = w.Write([]byte("[{\"id\":\"" + attachPostID + "\",\"author_id\":\"" + attachUserID + "\",\"user_id\":null}]"))
		case r.URL.Path == "/rest/v1/media_assets" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte("[{\"id\":\"" + attachMediaID + "\",\"owner_id\":\"" + attachUserID + "\",\"status\":\"uploaded\",\"post_id\":null,\"media_url\":\"https://media.testagram.site/v1/users/x/file.jpg\",\"media_type\":\"image\",\"mime_type\":\"image/jpeg\",\"byte_size\":1024}]"))
		case r.URL.Path == "/rest/v1/media_assets" && r.Method == http.MethodPatch:
			patchCalls++
			if r.URL.Query().Get("owner_id") != "eq."+attachUserID || r.URL.Query().Get("status") != "eq.uploaded" {
				t.Errorf("PATCH missing owner/status guard: %v", r.URL.Query())
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["post_id"] != attachPostID {
				t.Errorf("invalid PATCH body: %#v err=%v", body, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("SUPABASE_URL", upstream.URL)
	s := &service{upstream: upstream.Client()}
	req := httptest.NewRequest(http.MethodPost, "/api/media/attach", strings.NewReader("{\"media_id\":\""+attachMediaID+"\",\"media_asset_id\":\""+attachMediaID+"\",\"post_id\":\""+attachPostID+"\"}"))
	req.Header.Set("Authorization", "Bearer user-session-token")
	req.Header.Set("Origin", "https://testagram.site")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if patchCalls != 1 {
		t.Fatalf("PATCH calls=%d, want 1", patchCalls)
	}
}

func TestMediaAttachRejectsPostOwnedByAnotherUser(t *testing.T) {
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "test-publishable-key")
	t.Setenv("SUPABASE_SERVICE_ROLE_KEY", "server-only-service-role")
	var patchCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			_, _ = w.Write([]byte("{\"id\":\"" + attachUserID + "\"}"))
		case "/rest/v1/posts":
			_, _ = w.Write([]byte("[{\"id\":\"" + attachPostID + "\",\"author_id\":\"44444444-4444-4444-8444-444444444444\",\"user_id\":null}]"))
		case "/rest/v1/media_assets":
			if r.Method == http.MethodPatch {
				patchCalls++
			}
			_, _ = w.Write([]byte("[{\"id\":\"" + attachMediaID + "\",\"owner_id\":\"" + attachUserID + "\",\"status\":\"uploaded\"}]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("SUPABASE_URL", upstream.URL)
	s := &service{upstream: upstream.Client()}
	req := httptest.NewRequest(http.MethodPost, "/api/media/attach", strings.NewReader("{\"media_id\":\""+attachMediaID+"\",\"post_id\":\""+attachPostID+"\"}"))
	req.Header.Set("Authorization", "Bearer user-session-token")
	req.Header.Set("Origin", "https://testagram.site")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404: %s", rec.Code, rec.Body.String())
	}
	if patchCalls != 0 {
		t.Fatalf("unauthorized attachment attempted %d PATCH calls", patchCalls)
	}
}
