package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/datacentreclient"
	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

func TestIdentityEvidenceUsesPrivateDataCentreLifecycle(t *testing.T) {
	const token = "identity-evidence-private-service-token-012345"
	const owner = "00000000-0000-4000-8000-000000000001"
	const key = "identity/" + owner + "/10000000-0000-4000-8000-000000000002/20000000-0000-4000-8000-000000000003.jpg-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	deleted := false
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/identity-evidence/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready", "namespace": "identity-evidence-private", "provider": "backblaze_b2", "primary": "verified"})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/upload-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Testagram-API-Version") != "1" {
			t.Fatal("Data Centre upload request did not carry the private service credentials")
		}
		var input struct {
			OwnerID     string `json:"owner_id"`
			Namespace   string `json:"namespace"`
			Key         string `json:"key"`
			ContentType string `json:"content_type"`
			SizeBytes   int64  `json:"size_bytes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.OwnerID != owner || input.Namespace != identityEvidenceNamespace || input.Key != key || input.ContentType != "image/jpeg" || input.SizeBytes != 1024 {
			t.Fatalf("unexpected identity evidence upload request: %+v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"intent_id": "intent-1", "upload_url": "https://s3.us-west-004.backblazeb2.com/signed-upload", "expires_at": time.Now().Add(10 * time.Minute), "max_bytes": 1024, "primary": "backblaze_b2"})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/verify", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "protected", "sha256": digest})
	})
	mux.HandleFunc("GET /internal/v1/identity-evidence/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("owner_id") != owner || r.URL.Query().Get("namespace") != identityEvidenceNamespace || r.URL.Query().Get("key") != key {
			t.Fatalf("unexpected evidence status query: %s", r.URL.RawQuery)
		}
		state := "protected"
		if deleted {
			state = "deleted"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": state, "content_type": "image/jpeg", "size_bytes": 1024, "sha256": digest})
	})
	mux.HandleFunc("POST /internal/v1/identity-evidence/delete", func(w http.ResponseWriter, r *http.Request) {
		deleted = true
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "delete_pending"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := datacentreclient.NewIdentityEvidenceClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	svc := &service{identityEvidenceMedia: client}
	if !svc.identityEvidenceStorageConfigured() {
		t.Fatal("configured Data Centre media client was not recognized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.identityEvidenceStorageAvailable(ctx); err != nil {
		t.Fatalf("storage readiness: %v", err)
	}
	uploadURL, err := svc.identityEvidenceCreateUploadURL(ctx, owner, key, "image/jpeg", 1024)
	if err != nil || uploadURL != "https://s3.us-west-004.backblazeb2.com/signed-upload" {
		t.Fatalf("upload URL = %q, %v", uploadURL, err)
	}
	if err := svc.identityEvidenceVerifyAndProtect(ctx, owner, key, "image/jpeg", 1024, digest); err != nil {
		t.Fatalf("verify and replicate: %v", err)
	}
	if err := svc.identityEvidenceDelete(ctx, owner, key); err != nil {
		t.Fatalf("durable delete: %v", err)
	}
	if !deleted {
		t.Fatal("Data Centre delete was not requested")
	}
	if strings.Contains(uploadURL, "media.internal") {
		t.Fatal("internal media hostname leaked into the upload grant")
	}
}

func TestIdentityEvidenceRequiresDedicatedDataCentreClient(t *testing.T) {
	svc := &service{
		virtualStorage:  &virtualstorage.Manager{},
		mediaReplicator: &virtualstorage.Replicator{},
	}
	if svc.identityEvidenceStorageConfigured() {
		t.Fatal("Backend-local virtual storage must not satisfy identity evidence readiness")
	}
	if err := svc.identityEvidenceStorageAvailable(context.Background()); err == nil {
		t.Fatal("identity evidence readiness accepted a missing dedicated Data Centre client")
	}
}

func TestIdentityEvidenceRoutesAreAbsentWithoutDedicatedDataCentreClient(t *testing.T) {
	handler := (&service{}).routes()
	paths := []string{
		"/api/identity/sessions/00000000-0000-4000-8000-000000000001/evidence/upload-url",
		"/api/identity/sessions/00000000-0000-4000-8000-000000000001/evidence/20000000-0000-4000-8000-000000000002/upload",
		"/api/identity/sessions/00000000-0000-4000-8000-000000000001/evidence/submit",
		"/api/admin/identity/sessions/00000000-0000-4000-8000-000000000001/evidence/20000000-0000-4000-8000-000000000002",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("identity evidence route %s is exposed without dedicated Data Centre storage: status=%d body=%s", path, rec.Code, rec.Body.String())
			}
		})
	}
}
