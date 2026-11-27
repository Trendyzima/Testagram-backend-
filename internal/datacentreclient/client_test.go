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
		var input downloadRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil { t.Fatal(err) }
		if input.Method != http.MethodGet && input.Method != http.MethodHead { t.Fatalf("unexpected delivery method: %q", input.Method) }
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
	head, err := client.HeadURL(ctx, "user-456", "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef")
	if err != nil || head.Provider != "cloudflare_r2" { t.Fatalf("HeadURL = %+v, %v", head, err) }
	deleted, err := client.Delete(ctx, "user-123", "user-123", "media-public", "users/user-123/0123456789abcdef0123456789abcdef")
	if err != nil || deleted != "delete_pending" {
		t.Fatalf("Delete = %q, %v", deleted, err)
	}
}

func TestProviderURLsMustBePublicHTTPSOrigins(t *testing.T) {
	for _, raw := range []string{
		"https://b2.example/object?X-Amz-Signature=abc",
		"https://r2.example/object",
	} {
		if !validProviderURL(raw) { t.Fatalf("valid provider URL rejected: %s", raw) }
	}
	for _, raw := range []string{
		"http://b2.example/object",
		"https://127.0.0.1/object",
		"https://10.0.0.2/object",
		"https://localhost/object",
		"https://media.internal/object",
		"https://b2.example:8080/object",
		"https://user:pass@b2.example/object",
	} {
		if validProviderURL(raw) { t.Fatalf("unsafe provider URL accepted: %s", raw) }
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

func TestPrivateDockerMediaServiceHostnameIsAllowed(t *testing.T) {
	if !isPrivateServiceHost("media") {
		t.Fatal("private Docker media service hostname must be accepted")
	}
	if isPrivateServiceHost("media.example.com") {
		t.Fatal("public media hostname must not be treated as private")
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

func TestCreateUploadURLRejectsUnsafeOrOverlongGrant(t *testing.T) {
	for _, tc := range []struct{name, uploadURL string; expiry time.Time}{
		{"private origin", "https://127.0.0.1/internal", time.Now().Add(10*time.Minute)},
		{"plaintext", "http://b2.example/object", time.Now().Add(10*time.Minute)},
		{"overlong expiry", "https://b2.example/object", time.Now().Add(24*time.Hour)},
		{"expired", "https://b2.example/object", time.Now().Add(-time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /internal/v1/media/upload-url", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(UploadGrant{IntentID:"intent-1",UploadURL:tc.uploadURL,ExpiresAt:tc.expiry,MaxBytes:128,Primary:"backblaze_b2"})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			client, err := New(server.URL, testToken, nil)
			if err != nil { t.Fatal(err) }
			if _, err := client.CreateUploadURL(context.Background(),"user-123","media-public","users/user-123/0123456789abcdef0123456789abcdef","image/jpeg",128); err == nil {
				t.Fatal("unsafe upload grant accepted")
			}
		})
	}
}

func TestMediaReadyUsesAuthenticatedMediaReadinessRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/media/readyz" {
			t.Fatalf("unexpected readiness request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("X-Testagram-API-Version") != "1" {
			t.Fatalf("missing media readiness authentication headers: %+v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready","namespace":"media"}`))
	}))
	defer server.Close()
	client, err := New(server.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ready(context.Background()); err != nil {
		t.Fatalf("media Ready = %v", err)
	}
}

func TestIdentityEvidenceClientUsesSeparateTokenAndRoutePrefix(t *testing.T) {
	const token = "identity-evidence-dedicated-service-token-012345"
	const owner = "00000000-0000-4000-8000-000000000001"
	key := "identity/" + owner + "/10000000-0000-4000-8000-000000000002/20000000-0000-4000-8000-000000000003.jpg-" + strings.Repeat("a", 43)
	digest := strings.Repeat("b", 64)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/identity-evidence/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ready", "namespace": "identity-evidence-private",
			"provider": "backblaze_b2", "primary": "verified",
		})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/upload-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token { t.Fatal("identity evidence token was not used") }
		_ = json.NewEncoder(w).Encode(UploadGrant{IntentID:"identity-intent",UploadURL:"https://s3.us-west-004.backblazeb2.com/identity",ExpiresAt:time.Now().Add(5*time.Minute),MaxBytes:1024,Primary:"backblaze_b2"})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token { t.Fatal("identity evidence verify used the wrong token") }
		_ = json.NewEncoder(w).Encode(Verification{Status:"replication_pending",SHA256:digest})
	})
	mux.HandleFunc("GET /internal/v1/identity-evidence/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Get("namespace") != "identity-evidence-private" { t.Fatal("identity evidence status escaped its route/token boundary") }
		_ = json.NewEncoder(w).Encode(MediaStatus{Status:"protected",ContentType:"image/jpeg",SizeBytes:1024,SHA256:digest})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/download-url", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://s3.us-west-004.backblazeb2.com/identity-download", "provider": "backblaze_b2"})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token { t.Fatal("identity evidence delete used the wrong token") }
		_ = json.NewEncoder(w).Encode(map[string]string{"status":"delete_pending"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := NewIdentityEvidenceClient(server.URL, token, server.Client())
	if err != nil { t.Fatal(err) }
	ctx := context.Background()
	if err := client.Ready(ctx); err != nil {
		t.Fatalf("identity evidence readiness: %v", err)
	}
	grant, err := client.CreateUploadURL(ctx, owner, "identity-evidence-private", key, "image/jpeg", 1024)
	if err != nil || grant.IntentID != "identity-intent" { t.Fatalf("identity evidence upload grant = %+v, %v", grant, err) }
	verified, err := client.Verify(ctx, owner, "identity-evidence-private", key, "image/jpeg", digest, 1024)
	if err != nil || verified.Status != "replication_pending" { t.Fatalf("identity evidence verify = %+v, %v", verified, err) }
	status, err := client.Status(ctx, owner, "identity-evidence-private", key)
	if err != nil || status.Status != "protected" { t.Fatalf("identity evidence status = %+v, %v", status, err) }
	download, err := client.DownloadURL(ctx, owner, owner, "identity-evidence-private", key)
	if err != nil || download.Provider != "backblaze_b2" { t.Fatalf("B2-only identity evidence download = %+v, %v", download, err) }
	deleted, err := client.Delete(ctx, owner, owner, "identity-evidence-private", key)
	if err != nil || deleted != "delete_pending" { t.Fatalf("identity evidence delete = %q, %v", deleted, err) }
}


func TestIdentityEvidenceRejectsNonB2Readiness(t *testing.T) {
	for _, response := range []string{
		`{"status":"ready","namespace":"identity-evidence-private","provider":"cloudflare_r2","primary":"verified"}`,
		`{"status":"ready","namespace":"media-private","provider":"backblaze_b2","primary":"verified"}`,
		`{"status":"ready","namespace":"identity-evidence-private","provider":"backblaze_b2","primary":"pending"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(response))
		}))
		client, err := NewIdentityEvidenceClient(server.URL, testToken, server.Client())
		if err != nil { server.Close(); t.Fatal(err) }
		if err := client.Ready(context.Background()); err == nil {
			server.Close()
			t.Fatalf("identity evidence accepted unsafe readiness response: %s", response)
		}
		server.Close()
	}
}

func TestIdentityEvidenceRejectsR2DownloadGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://r2.example/identity-object","provider":"cloudflare_r2"}`))
	}))
	defer server.Close()
	client, err := NewIdentityEvidenceClient(server.URL, testToken, server.Client())
	if err != nil { t.Fatal(err) }
	if _, err := client.DownloadURL(context.Background(), "requester", "owner", "identity-evidence-private", "identity/object"); err == nil {
		t.Fatal("identity evidence accepted a Cloudflare R2 download grant")
	}
}


func TestValidB2ProviderURLRequiresBackblazeS3Host(t *testing.T) {
	for _, raw := range []string{
		"https://s3.us-west-004.backblazeb2.com/bucket/object",
		"https://s3.eu-central-003.backblazeb2.com/bucket/object",
	} {
		if !validB2ProviderURL(raw) { t.Fatalf("valid Backblaze B2 URL rejected: %s", raw) }
	}
	for _, raw := range []string{
		"https://r2.example/object",
		"https://s3.amazonaws.com/bucket/object",
		"https://backblazeb2.com.evil.example/object",
		"http://s3.us-west-004.backblazeb2.com/object",
	} {
		if validB2ProviderURL(raw) { t.Fatalf("non-Backblaze URL accepted as B2: %s", raw) }
	}
}

func TestIdentityEvidenceRejectsB2LabelOnNonBackblazeHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://r2.example/identity-object","provider":"backblaze_b2"}`))
	}))
	defer server.Close()
	client, err := NewIdentityEvidenceClient(server.URL, testToken, server.Client())
	if err != nil { t.Fatal(err) }
	if _, err := client.DownloadURL(context.Background(), "requester", "owner", "identity-evidence-private", "identity/object"); err == nil {
		t.Fatal("identity evidence accepted a non-Backblaze host labelled as B2")
	}
}
