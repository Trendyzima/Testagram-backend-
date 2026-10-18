package virtualstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestS3ProviderPresignsScopedOperations(t *testing.T) {
	p, err := NewS3Provider(S3Config{Endpoint:"https://objects.example.test", AccessKey:"access", SecretKey:"secret", Region:"us-east-1"})
	if err != nil { t.Fatal(err) }
	put, err := p.PresignPut(context.Background(), "private-media", "tenants/u1/a.jpg", "image/jpeg", 1024, 5*time.Minute)
	if err != nil { t.Fatal(err) }
	u, err := url.Parse(put)
	if err != nil { t.Fatal(err) }
	if u.Host!="objects.example.test" || u.Path!="/private-media/tenants/u1/a.jpg" || u.Query().Get("X-Amz-SignedHeaders")!="content-type;host" || len(u.Query().Get("X-Amz-Signature"))!=64 {
		t.Fatalf("unexpected signed PUT URL: %s", put)
	}
	get, err := p.PresignGet(context.Background(), "private-media", "tenants/u1/a.jpg", time.Minute)
	if err != nil { t.Fatal(err) }
	gu, _ := url.Parse(get)
	if gu.Query().Get("X-Amz-SignedHeaders")!="host" || gu.Query().Get("X-Amz-Signature")=="" { t.Fatalf("invalid signed GET URL: %s", get) }
	if strings.Contains(put, "secret") { t.Fatal("secret key leaked into signed URL") }
}

func TestS3ProviderListParsesContinuationAndMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method!="GET" || r.URL.Query().Get("list-type")!="2" || r.URL.Query().Get("prefix")!="tenants/u1/" { t.Errorf("unexpected list request: %s %s", r.Method, r.URL.String()) }
		w.Header().Set("Content-Type","application/xml")
		_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>tenants/u1/a.jpg</Key><Size>12</Size><ETag>"opaque"</ETag><LastModified>2026-10-18T10:00:00Z</LastModified></Contents></ListBucketResult>`))
	}))
	defer server.Close()
	p, err := NewS3Provider(S3Config{Endpoint:server.URL, AccessKey:"access", SecretKey:"secret"})
	if err != nil { t.Fatal(err) }
	items, cursor, err := p.List(context.Background(), "private-media", "tenants/u1/", "", 10)
	if err != nil { t.Fatal(err) }
	if len(items)!=1 || items[0].Key!="tenants/u1/a.jpg" || items[0].Size!=12 || items[0].ETag!="opaque" || cursor!="next" { t.Fatalf("unexpected list result: %+v cursor=%q",items,cursor) }
}

func TestNewS3ProviderAllowsTrustedReverseProxySigningHost(t *testing.T) {
	p, err := NewS3Provider(S3Config{Endpoint:"https://media.example.test", SigningHost:"object-storage:9000", AccessKey:"access", SecretKey:"secret"})
	if err != nil { t.Fatalf("expected reverse-proxy signing host to be accepted: %v", err) }
	put, err := p.PresignPut(context.Background(), "private-media", "tenants/u1/a.jpg", "image/jpeg", 12, time.Minute)
	if err != nil { t.Fatal(err) }
	u, err := url.Parse(put)
	if err != nil { t.Fatal(err) }
	if u.Host != "media.example.test" || u.Query().Get("X-Amz-Signature") == "" { t.Fatalf("invalid public URL: %s", put) }
}

func TestS3ProviderDoesNotFollowSignedRedirects(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	p, err := NewS3Provider(S3Config{Endpoint: source.URL, AccessKey: "access", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Head(context.Background(), "private-media", "tenants/u1/a.jpg"); err == nil {
		t.Fatal("expected redirect response to fail the signed HEAD operation")
	}
	if targetHit {
		t.Fatal("signed object-store request followed a redirect to another host")
	}
}

func TestNewS3ProviderRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []S3Config{
		{Endpoint:"file:///tmp",AccessKey:"a",SecretKey:"b"},
		{Endpoint:"https://objects.example.test/prefix",AccessKey:"a",SecretKey:"b"},
		{Endpoint:"https://objects.example.test",AccessKey:"a"},
	} {
		if _, err := NewS3Provider(cfg); err==nil { t.Errorf("expected invalid config rejected: %+v",cfg) }
	}
}

func TestS3ProviderSHA256StreamsActualBytesAndEnforcesBound(t *testing.T) {
	payload := []byte("testagram-media-payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET for checksum, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "23")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	p, err := NewS3Provider(S3Config{Endpoint: server.URL, AccessKey: "access", SecretKey: "secret"})
	if err != nil { t.Fatal(err) }
	sum := sha256.Sum256(payload)
	got, err := p.SHA256(context.Background(), "private-media", "tenants/u1/a.jpg", 64)
	if err != nil { t.Fatal(err) }
	if got != hex.EncodeToString(sum[:]) { t.Fatalf("SHA-256 mismatch: got %s", got) }
	if _, err := p.SHA256(context.Background(), "private-media", "tenants/u1/a.jpg", 8); err != ErrTooLarge {
		t.Fatalf("expected bounded reader to reject oversized body, got %v", err)
	}
}

