package datacentreclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "data-centre-private-service-token-0123456789"

func TestClientCallsPrivateMediaLifecycleWithBearerAndVersionHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/media/upload-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("X-Testagram-API-Version") != "1" || r.Header.Get("X-Request-ID") == "" {
			t.Fatalf("missing private API headers: %+v", r.Header)
		}
		var input uploadRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.OwnerID != "user-123" || input.Namespace != "media-public" || input.SizeBytes != 2048 {
			t.Fatalf("unexpected upload request: %+v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(UploadGrant{
			IntentID: "intent-1", UploadURL: "https://b2.example/upload", ExpiresAt: time.Now().Add(10 * time.Minute),
			MaxBytes: input.SizeBytes, Primary: "backblaze_b2",
		})
	})
	mux.HandleFunc("POST /internal/v1/media/verify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"replication_pending","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`))
	})
	mux.HandleFunc("GET /internal/v1/media/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("owner_id") != "user-123" || r.URL.Query().Get("namespace") != "media-public" {
			t.Fatalf("unexpected status query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"protected","content_type":"image/jpeg","size_bytes":2048,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`))
	})
	mux.HandleFunc("POST /internal/v1/media/download-url", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://r2.example/object","provider":"cloudflare_r2"}`))
	})
	mux.HandleFunc("POST /internal/v1/media/delete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"delete_pending"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := New(server.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	grant, err := client.CreateUploadURL(ctx, "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef", "image/jpeg", 2048)
	if err != nil || grant.UploadURL == "" {
		t.Fatalf("CreateUploadURL = %+v, %v", grant, err)
	}
	verified, err := client.Verify(ctx, "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef", "image/jpeg", strings.Repeat("0", 64), 2048)
	if err != nil || verified.Status != "replication_pending" {
		t.Fatalf("Verify = %+v, %v", verified, err)
	}
	status, err := client.Status(ctx, "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef")
	if err != nil || status.Status != "protected" || status.SizeBytes != 2048 {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	download, err := client.DownloadURL(ctx, "user-456", "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef")
	if err != nil || download.Provider != "cloudflare_r2" {
		t.Fatalf("DownloadURL = %+v, %v", download, err)
	}
	deleted, err := client.Delete(ctx, "user-123", "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef")
	if err != nil || deleted != "delete_pending" {
		t.Fatalf("Delete = %q, %v", deleted, err)
	}
}

func TestNewRejectsPublicHTTPAndShortToken(t *testing.T) {
	if _, err := New("http://example.com", testToken, nil); err == nil {
		t.Fatal("public HTTP Data Centre endpoint was accepted")
	}
	if _, err := New("http://control-plane:8787", "short", nil); err == nil {
		t.Fatal("short private service token was accepted")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	client, err := New(redirect.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Status(context.Background(), "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef")
	if err == nil {
		t.Fatal("redirect response was accepted")
	}
	if followed {
		t.Fatal("Data Centre client followed an untrusted redirect")
	}
}
