package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mediasigner "github.com/Trendyzima/Testagram-backend-/internal/media"
)

func TestCDNPurgerAcceptsPurgedAndNotCached(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			token := strings.Repeat("p", 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "wrong method", http.StatusMethodNotAllowed)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+token {
					http.Error(w, "wrong authorization", http.StatusUnauthorized)
					return
				}
				if got := r.URL.Query().Get("path"); got != "users/owner/key with space.webp" {
					http.Error(w, "wrong path", http.StatusBadRequest)
					return
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			purger, err := newCDNPurger(server.URL+"/api/cache/purge", token)
			if err != nil {
				t.Fatalf("newCDNPurger: %v", err)
			}
			if err := purger.Purge(context.Background(), "users/owner/key with space.webp"); err != nil {
				t.Fatalf("Purge returned error for HTTP %d: %v", status, err)
			}
		})
	}
}

func TestCDNPurgerRejectsUnauthorizedServerFailureAndRedirect(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var redirectedRequests int
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				redirectedRequests++
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusFound {
					w.Header().Set("Location", target.URL+"/steal-token")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			purger, err := newCDNPurger(server.URL+"/api/cache/purge", strings.Repeat("p", 32))
			if err != nil {
				t.Fatalf("newCDNPurger: %v", err)
			}
			if err := purger.Purge(context.Background(), "users/owner/0123456789abcdef0123456789abcdef"); err == nil {
				t.Fatalf("Purge unexpectedly accepted HTTP %d", status)
			}
			if redirectedRequests != 0 {
				t.Fatal("purge token was forwarded across an HTTP redirect")
			}
		})
	}
}

func TestNewCDNPurgerRequiresPairedStrongCredentialsAndSafeURL(t *testing.T) {
	if _, err := newCDNPurger("http://media-cache:8081/api/cache/purge", ""); err == nil {
		t.Fatal("expected missing token to be rejected")
	}
	if _, err := newCDNPurger("", strings.Repeat("p", 32)); err == nil {
		t.Fatal("expected missing URL to be rejected")
	}
	if _, err := newCDNPurger("http://user:pass@media-cache:8081/api/cache/purge", strings.Repeat("p", 32)); err == nil {
		t.Fatal("expected URL userinfo to be rejected")
	}
	if _, err := newCDNPurger("https://media-cache:8081/other", strings.Repeat("p", 32)); err == nil {
		t.Fatal("expected wrong purge route to be rejected")
	}
	if _, err := newCDNPurger("http://media-cache:8081/api/cache/purge", strings.Repeat("p", 31)); err == nil {
		t.Fatal("expected weak purge token to be rejected")
	}
	if purger, err := newCDNPurger("", ""); err != nil || purger != nil {
		t.Fatalf("empty optional config should disable purger, got purger=%v err=%v", purger, err)
	}
}

func TestDeleteMediaObjectPurgesBeforeDeletingStorage(t *testing.T) {
	var mu sync.Mutex
	var order []string
	purgeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, "purge")
		mu.Unlock()
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("p", 32) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.URL.Query().Get("path"); got != "users/owner/0123456789abcdef0123456789abcdef" {
			http.Error(w, "wrong key", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer purgeServer.Close()

	storageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, "delete")
		mu.Unlock()
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer storageServer.Close()

	signer, err := mediasigner.NewSigner(mediasigner.Config{
		Endpoint: storageServer.URL,
		SigningHost: strings.TrimPrefix(storageServer.URL, "http://"),
		AccessKey: "ci-media-access",
		SecretKey: strings.Repeat("s", 32),
		Region: "us-east-1",
		PrivateBucket: "ci-private-media",
		PublicBucket: "ci-public-media",
	})
	if err != nil {
		t.Fatalf("create media signer: %v", err)
	}
	purger, err := newCDNPurger(purgeServer.URL+"/api/cache/purge", strings.Repeat("p", 32))
	if err != nil {
		t.Fatalf("create CDN purger: %v", err)
	}
	svc := &service{media: signer, cdnPurger: purger, upstream: storageServer.Client()}
	if err := svc.deleteMediaObjectWithNamespace(context.Background(), "owner", "users/owner/0123456789abcdef0123456789abcdef", "public", "legacy-media", "default"); err != nil {
		t.Fatalf("deleteMediaObjectWithNamespace: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "purge" || order[1] != "delete" {
		t.Fatalf("operations ran in unsafe order: %#v", order)
	}
}

func TestDeleteMediaObjectDoesNotDeleteStorageWhenPurgeFails(t *testing.T) {
	var storageDeletes int
	purgeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer purgeServer.Close()
	storageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageDeletes++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer storageServer.Close()

	signer, err := mediasigner.NewSigner(mediasigner.Config{
		Endpoint: storageServer.URL,
		SigningHost: strings.TrimPrefix(storageServer.URL, "http://"),
		AccessKey: "ci-media-access",
		SecretKey: strings.Repeat("s", 32),
		Region: "us-east-1",
		PrivateBucket: "ci-private-media",
		PublicBucket: "ci-public-media",
	})
	if err != nil {
		t.Fatalf("create media signer: %v", err)
	}
	purger, err := newCDNPurger(purgeServer.URL+"/api/cache/purge", strings.Repeat("p", 32))
	if err != nil {
		t.Fatalf("create CDN purger: %v", err)
	}
	svc := &service{media: signer, cdnPurger: purger, upstream: storageServer.Client()}
	if err := svc.deleteMediaObjectWithNamespace(context.Background(), "owner", "users/owner/0123456789abcdef0123456789abcdef", "public", "legacy-media", "default"); err == nil {
		t.Fatal("expected deletion to remain pending when CDN purge fails")
	}
	if storageDeletes != 0 {
		t.Fatalf("storage deletion ran %d times despite failed CDN purge", storageDeletes)
	}
}
