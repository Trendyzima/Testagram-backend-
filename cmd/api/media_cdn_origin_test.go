package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	mediasigner "github.com/Trendyzima/Testagram-backend-/internal/media"
	"github.com/Trendyzima/Testagram-backend-/internal/mediaassertion"
)

func TestCDNOriginObjectSize(t *testing.T) {
	tests := []struct {
		name        string
		contentLen  int64
		contentRange string
		want        int64
		wantErr     bool
	}{
		{name: "full object", contentLen: 4096, want: 4096},
		{name: "partial response total", contentLen: 1024, contentRange: "bytes 0-1023/8192", want: 8192},
		{name: "unsatisfied range total", contentLen: 0, contentRange: "bytes */8192", want: 8192},
		{name: "unknown length", contentLen: -1, wantErr: true},
		{name: "malformed range", contentLen: 10, contentRange: "bytes 0-9/unknown", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{ContentLength: tt.contentLen, Header: make(http.Header)}
			if tt.contentRange != "" {
				resp.Header.Set("Content-Range", tt.contentRange)
			}
			got, err := cdnOriginObjectSize(resp)
			if (err != nil) != tt.wantErr {
				t.Fatalf("cdnOriginObjectSize() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("cdnOriginObjectSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCDNOriginRequestForwardsRangeValidators(t *testing.T) {
	source := httptest.NewRequest(http.MethodGet, "/internal/cdn/v1/users/u1/video.mp4", nil)
	source.Header.Set("Range", "bytes=10-19")
	source.Header.Set("If-Range", `"media-v1"`)
	source.Header.Set("If-None-Match", `"media-v1"`)
	destination := httptest.NewRequest(http.MethodGet, "https://storage.example/object", nil)

	copyCDNOriginRequestHeaders(source, destination)

	for name, want := range map[string]string{
		"Range": "bytes=10-19",
		"If-Range": `"media-v1"`,
		"If-None-Match": `"media-v1"`,
	} {
		if got := destination.Header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestWriteCDNOriginResponsePreservesPartialRange(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusPartialContent,
		Header: http.Header{
			"Content-Type": []string{"video/mp4"},
			"Content-Length": []string{"5"},
			"Content-Range": []string{"bytes 10-14/100"},
			"Accept-Ranges": []string{"bytes"},
			"ETag": []string{`"media-v1"`},
		},
		Body: io.NopCloser(strings.NewReader("abcde")),
	}
	req := httptest.NewRequest(http.MethodGet, "/media/video", nil)
	rec := httptest.NewRecorder()

	writeCDNOriginResponse(rec, req, resp, "users/u1/video.mp4", "private")

	if rec.Code != http.StatusPartialContent || rec.Body.String() != "abcde" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{
		"Content-Range": "bytes 10-14/100",
		"Accept-Ranges": "bytes",
		"Content-Length": "5",
		"Cache-Control": "private, no-store",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestWriteCDNOriginResponseHEADHasHeadersButNoBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"video/mp4"},
			"Content-Length": []string{"1048576"},
			"Accept-Ranges": []string{"bytes"},
		},
		Body: io.NopCloser(strings.NewReader("must-not-be-written")),
	}
	req := httptest.NewRequest(http.MethodHead, "/media/video", nil)
	rec := httptest.NewRecorder()

	writeCDNOriginResponse(rec, req, resp, "users/u1/video.mp4", "private")

	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "1048576" {
		t.Fatalf("HEAD status=%d bytes=%d headers=%+v", rec.Code, rec.Body.Len(), rec.Header())
	}
}

func TestWriteCDNOriginResponsePublicUsesSharedCachePolicy(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"image/webp"}, "Content-Length": []string{"3"}},
		Body: io.NopCloser(strings.NewReader("img")),
	}
	req := httptest.NewRequest(http.MethodGet, "/media/image", nil)
	rec := httptest.NewRecorder()
	writeCDNOriginResponse(rec, req, resp, "users/u1/image", "public")
	if rec.Header().Get("X-Testagram-Visibility") != "public" || rec.Header().Get("Cache-Control") != "public, max-age=0, s-maxage=300" {
		t.Fatalf("public origin cache policy is unsafe: %v", rec.Header())
	}
	if rec.Body.String() != "img" { t.Fatalf("body = %q", rec.Body.String()) }
}


// TestMediaCDNOriginPostgresIntegration exercises the actual origin handler
// against PostgreSQL metadata and a signed S3-compatible test origin. It is
// enabled in CI with an isolated database containing the media migrations.
func TestMediaCDNOriginPostgresIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TESTAGRAM_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TESTAGRAM_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open media-origin PostgreSQL pool: %v", err)
	}
	ownerID := fmt.Sprintf("cdnci-%x", time.Now().UnixNano())
	viewerID := "cdn_viewer_" + strings.TrimPrefix(ownerID, "cdnci-")
	publicKey := "users/" + ownerID + "/" + strings.Repeat("1", 32)
	privateKey := "users/" + ownerID + "/" + strings.Repeat("2", 32)
	quarantinedKey := "users/" + ownerID + "/" + strings.Repeat("3", 32)
	deletedKey := "users/" + ownerID + "/" + strings.Repeat("5", 32)
	mismatchedKey := "users/different-owner/" + strings.Repeat("4", 32)

	insertAsset := func(key, owner, visibility, status string) {
		t.Helper()
		_, insertErr := db.Exec(ctx, `
INSERT INTO testagram_media.assets
  (object_key,owner_id,visibility,content_type,size_bytes,etag,status,storage_namespace,storage_provider,checksum_sha256)
VALUES ($1,$2,$3,'image/webp',3,'ci-etag',$4,'legacy-media','default','')`, key, owner, visibility, status)
		if insertErr != nil {
			t.Fatalf("insert CDN origin asset %s: %v", key, insertErr)
		}
	}
	insertAsset(publicKey, ownerID, "public", "ready")
	insertAsset(privateKey, ownerID, "private", "ready")
	insertAsset(quarantinedKey, ownerID, "public", "quarantined")
	insertAsset(deletedKey, ownerID, "public", "deleted")
	insertAsset(mismatchedKey, ownerID, "public", "ready")
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_media.assets WHERE owner_id=$1", ownerID)
		db.Close()
	}()

	var mu sync.Mutex
	var seenMethods []string
	var seenRanges []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenMethods = append(seenMethods, r.Method)
		seenRanges = append(seenRanges, r.Header.Get("Range"))
		mu.Unlock()
		if r.URL.Query().Get("X-Amz-Signature") == "" || r.URL.Query().Get("X-Amz-SignedHeaders") != "host" {
			http.Error(w, "missing method-correct signed URL", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"cdn-ci-etag"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "3")
			return
		}
		if r.Header.Get("Range") == "bytes=1-2" {
			w.Header().Set("Content-Length", "2")
			w.Header().Set("Content-Range", "bytes 1-2/3")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("mg"))
			return
		}
		w.Header().Set("Content-Length", "3")
		_, _ = w.Write([]byte("img"))
	}))
	defer storage.Close()

	signer, err := mediasigner.NewSigner(mediasigner.Config{
		Endpoint: storage.URL,
		SigningHost: strings.TrimPrefix(storage.URL, "http://"),
		AccessKey: "ci-media-access",
		SecretKey: strings.Repeat("s", 32),
		Region: "us-east-1",
		PrivateBucket: "ci-private-media",
		PublicBucket: "ci-public-media",
	})
	if err != nil {
		t.Fatalf("create media signer: %v", err)
	}
	assertions, err := mediaassertion.New([]byte(strings.Repeat("a", 32)), 2*time.Minute)
	if err != nil {
		t.Fatalf("create CDN assertion issuer: %v", err)
	}
	originToken := strings.Repeat("o", 32)
	svc := &service{
		db: db,
		media: signer,
		cdnMediaAssertions: assertions,
		cdnOriginAuthToken: originToken,
		upstream: storage.Client(),
	}

	call := func(method, key, serviceToken, viewerAssertion, rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/internal/cdn/v1/"+key, nil)
		req.SetPathValue("key", key)
		if serviceToken != "" {
			req.Header.Set("X-Testagram-Origin-Auth", serviceToken)
		}
		if viewerAssertion != "" {
			req.Header.Set("X-Testagram-Viewer-Authorization", "Bearer "+viewerAssertion)
		}
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		rec := httptest.NewRecorder()
		svc.mediaCDNOrigin(rec, req)
		return rec
	}

	if got := call(http.MethodGet, publicKey, "wrong-origin-token", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong origin credential status=%d, want 401", got.Code)
	}
	if got := call(http.MethodGet, quarantinedKey, originToken, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("quarantined asset status=%d, want 404", got.Code)
	}
	if got := call(http.MethodGet, deletedKey, originToken, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("deleted asset status=%d, want 404", got.Code)
	}
	if got := call(http.MethodGet, mismatchedKey, originToken, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("owner/key mismatch status=%d, want 404", got.Code)
	}
	if got := call(http.MethodPost, publicKey, originToken, "", ""); got.Code != http.StatusMethodNotAllowed || got.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("unsupported method status=%d allow=%q, want 405 GET, HEAD", got.Code, got.Header().Get("Allow"))
	}
	if got := call(http.MethodGet, privateKey, originToken, "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("private asset without viewer assertion status=%d, want 401", got.Code)
	}
	if got := call(http.MethodGet, privateKey, originToken, "not-a-valid-assertion", ""); got.Code != http.StatusForbidden {
		t.Fatalf("private asset with invalid viewer assertion status=%d, want 403", got.Code)
	}

	assertion, _, err := assertions.IssueForViewer(privateKey, ownerID, viewerID, time.Now())
	if err != nil {
		t.Fatalf("issue viewer-bound assertion: %v", err)
	}
	privateResponse := call(http.MethodGet, privateKey, originToken, assertion, "")
	if privateResponse.Code != http.StatusOK || privateResponse.Body.String() != "img" {
		t.Fatalf("authorized private media status=%d body=%q", privateResponse.Code, privateResponse.Body.String())
	}
	if privateResponse.Header().Get("X-Testagram-Visibility") != "private" || privateResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private response was not isolated from shared caches: %v", privateResponse.Header())
	}

	publicResponse := call(http.MethodGet, publicKey, originToken, "", "")
	if publicResponse.Code != http.StatusOK || publicResponse.Body.String() != "img" {
		t.Fatalf("public media status=%d body=%q", publicResponse.Code, publicResponse.Body.String())
	}
	if publicResponse.Header().Get("X-Testagram-Visibility") != "public" || publicResponse.Header().Get("Cache-Control") != "public, max-age=0, s-maxage=300" {
		t.Fatalf("public response cache classification is incorrect: %v", publicResponse.Header())
	}

	rangeResponse := call(http.MethodGet, publicKey, originToken, "", "bytes=1-2")
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "mg" || rangeResponse.Header().Get("Content-Range") != "bytes 1-2/3" {
		t.Fatalf("range response status=%d body=%q headers=%v", rangeResponse.Code, rangeResponse.Body.String(), rangeResponse.Header())
	}
	headResponse := call(http.MethodHead, publicKey, originToken, "", "")
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") != "3" {
		t.Fatalf("HEAD response status=%d body=%d headers=%v", headResponse.Code, headResponse.Body.Len(), headResponse.Header())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenMethods) != 4 {
		t.Fatalf("storage origin received %d requests, want only authorized GET/Range/HEAD calls (4)", len(seenMethods))
	}
	if seenMethods[0] != http.MethodGet || seenMethods[1] != http.MethodGet || seenMethods[2] != http.MethodGet || seenMethods[3] != http.MethodHead {
		t.Fatalf("unexpected storage methods: %#v", seenMethods)
	}
	if seenRanges[2] != "bytes=1-2" {
		t.Fatalf("Range header was not forwarded to storage: %#v", seenRanges)
	}
}
