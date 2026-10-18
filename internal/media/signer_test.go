package media

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testSigner(t *testing.T, endpoint string) *Signer {
	t.Helper()
	signer, err := NewSigner(Config{
		Endpoint: endpoint, SigningHost: "object-storage:9000", AccessKey: "test-access",
		SecretKey: "test-secret-never-returned", Region: "us-east-1",
		PrivateBucket: "private-media", PublicBucket: "public-media",
		MaxImageBytes: 10 << 20, MaxVideoBytes: 100 << 20,
		UploadTTL: 10 * time.Minute,
	})
	if err != nil { t.Fatal(err) }
	return signer
}

func TestUploadIntentSignsOwnerScopedPUTURL(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	now := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	intent, err := signer.CreateUploadIntent("user-123", "image/webp", "private", 1024, now)
	if err != nil { t.Fatal(err) }
	if intent.Method != "PUT" || intent.Bucket != "private-media" || intent.PublicURL != "" {
		t.Fatalf("unexpected private upload intent: %+v", intent)
	}
	if len(intent.Fields) != 0 {
		t.Fatalf("PUT upload must not require multipart form fields: %#v", intent.Fields)
	}
	if !strings.HasPrefix(intent.Key, "users/user-123/") || !objectKeyPattern.MatchString(intent.Key) {
		t.Fatalf("object key is not owner scoped: %q", intent.Key)
	}
	if !strings.HasPrefix(intent.URL, "https://media.example.test/private-media/") {
		t.Fatalf("unexpected upload URL: %q", intent.URL)
	}
	if strings.Contains(intent.URL, "test-secret-never-returned") {
		t.Fatal("secret key leaked into upload intent")
	}
	u, err := url.Parse(intent.URL)
	if err != nil { t.Fatal(err) }
	q := u.Query()
	if q.Get("X-Amz-SignedHeaders") != "content-type;host" || len(q.Get("X-Amz-Signature")) != 64 {
		t.Fatalf("upload URL is not signed for Content-Type: %v", q)
	}
	if q.Get("X-Amz-Expires") != "600" {
		t.Fatalf("unexpected upload expiry: %v", q)
	}
}

func TestUploadIntentPUTSignatureMatchesCanonicalRequest(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	now := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	intent, err := signer.CreateUploadIntent("user-123", "image/webp", "private", 1024, now)
	if err != nil { t.Fatal(err) }
	u, err := url.Parse(intent.URL)
	if err != nil { t.Fatal(err) }
	q := u.Query()
	gotSignature := q.Get("X-Amz-Signature")
	q.Del("X-Amz-Signature")
	scope := q.Get("X-Amz-Date")[:8] + "/" + signer.region + "/s3/aws4_request"
	canonicalHeaders := "content-type:image/webp\n" + "host:" + signer.signingHost + "\n"
	canonicalRequest := strings.Join([]string{"PUT", u.EscapedPath(), q.Encode(), canonicalHeaders, "content-type;host", "UNSIGNED-PAYLOAD"}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + q.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	wantSignature := hex.EncodeToString(hmacSHA256(signingKey(signer.secretKey, q.Get("X-Amz-Date")[:8], signer.region), []byte(stringToSign)))
	if gotSignature != wantSignature {
		t.Fatalf("PUT signature mismatch: got %q want %q", gotSignature, wantSignature)
	}
}

func TestPublicUploadIntentKeepsCDNURLOutOfStorageSigner(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	intent, err := signer.CreateUploadIntent("user-123", "video/mp4", "public", 1<<20, time.Now())
	if err != nil { t.Fatal(err) }
	if intent.Bucket != "public-media" || intent.PublicURL != "" {
		t.Fatalf("signer must return storage coordinates only; CDN URL is composed by API: %+v", intent)
	}
}

func TestUploadIntentRejectsUnsafeRequests(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	tests := []struct {
		name, userID, contentType, visibility string
		size int64
	}{
		{"path traversal user", "../other", "image/png", "private", 1},
		{"html upload", "user-1", "text/html", "public", 1},
		{"unsupported visibility", "user-1", "image/png", "shared", 1},
		{"image too large", "user-1", "image/png", "private", 11 << 20},
		{"empty object", "user-1", "image/png", "private", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := signer.CreateUploadIntent(test.userID, test.contentType, test.visibility, test.size, time.Now()); err == nil {
				t.Fatal("expected unsafe upload intent to be rejected")
			}
		})
	}
}

func TestPrivateDownloadSigningEnforcesOwnershipAndExpires(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	key := "users/user-123/0123456789abcdef0123456789abcdef"
	now := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	got, err := signer.PresignPrivateDownload("user-123", key, 5*time.Minute, now)
	if err != nil { t.Fatal(err) }
	u, err := url.Parse(got)
	if err != nil { t.Fatal(err) }
	if u.Host != "media.example.test" || !strings.HasPrefix(u.Path, "/private-media/users/user-123/") {
		t.Fatalf("unexpected private download URL: %s", got)
	}
	q := u.Query()
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Expires") != "300" || len(q.Get("X-Amz-Signature")) != 64 {
		t.Fatalf("missing or invalid signature parameters: %v", q)
	}
	if _, err := signer.PresignPrivateDownload("different-user", key, 5*time.Minute, now); err == nil {
		t.Fatal("cross-user private download URL must be rejected")
	}
	if _, err := signer.PresignPrivateDownload("user-123", key, 2*time.Hour, now); err == nil {
		t.Fatal("excessive URL lifetime must be rejected")
	}
}

func TestNewSignerRejectsMissingCredentialsAndBadEndpoint(t *testing.T) {
	if _, err := NewSigner(Config{Endpoint: "file:///tmp", AccessKey: "key", SecretKey: "secret"}); err == nil {
		t.Fatal("expected invalid endpoint to be rejected")
	}
	if _, err := NewSigner(Config{Endpoint: "https://media.example.test/prefix", AccessKey: "key", SecretKey: "secret"}); err == nil {
		t.Fatal("expected endpoint path prefix to be rejected")
	}
	if _, err := NewSigner(Config{Endpoint: "https://user:pass@media.example.test", AccessKey: "key", SecretKey: "secret"}); err == nil {
		t.Fatal("expected endpoint user info to be rejected")
	}
	if _, err := NewSigner(Config{Endpoint: "http://minio:9000", AccessKey: "key"}); err == nil {
		t.Fatal("expected missing secret to be rejected")
	}
}

func mapValues(m map[string]string) []string {
	values := make([]string, 0, len(m))
	for _, value := range m { values = append(values, value) }
	return values
}

func TestPresignObjectHeadIsOwnerScopedAndVisibilityScoped(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	now := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	key := "users/user-123/0123456789abcdef0123456789abcdef"
	got, err := signer.PresignObjectHead("user-123", key, "public", time.Minute, now)
	if err != nil { t.Fatal(err) }
	u, err := url.Parse(got)
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(u.Path, "/public-media/users/user-123/") {
		t.Fatalf("HEAD verification URL has wrong bucket/path: %q", u.Path)
	}
	if u.Query().Get("X-Amz-Signature") == "" || u.Query().Get("X-Amz-Expires") != "60" {
		t.Fatalf("HEAD verification URL is not signed or has wrong expiry: %v", u.Query())
	}
	if _, err := signer.PresignObjectHead("other-user", key, "public", time.Minute, now); err == nil {
		t.Fatal("cross-owner HEAD URL must be rejected")
	}
	if _, err := signer.PresignObjectHead("user-123", key, "unknown", time.Minute, now); err == nil {
		t.Fatal("invalid visibility must be rejected")
	}
	if max, ok := signer.MaxBytesForContentType("video/mp4"); !ok || max != 100<<20 {
		t.Fatalf("unexpected video max: %d %v", max, ok)
	}
	if _, ok := signer.MaxBytesForContentType("text/html"); ok {
		t.Fatal("HTML must not be accepted as media")
	}
}


func TestPresignObjectDeleteIsOwnerScopedAndVisibilityScoped(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	now := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	key := "users/user-123/0123456789abcdef0123456789abcdef"
	got, err := signer.PresignObjectDelete("user-123", key, "public", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.Path, "/public-media/users/user-123/") {
		t.Fatalf("DELETE URL has wrong bucket/path: %q", u.Path)
	}
	if u.Query().Get("X-Amz-Signature") == "" || u.Query().Get("X-Amz-Expires") != "60" {
		t.Fatalf("DELETE URL is not signed or has wrong expiry: %v", u.Query())
	}
	if _, err := signer.PresignObjectDelete("other-user", key, "public", time.Minute, now); err == nil {
		t.Fatal("cross-owner DELETE URL must be rejected")
	}
	if _, err := signer.PresignObjectDelete("user-123", key, "unknown", time.Minute, now); err == nil {
		t.Fatal("invalid visibility must be rejected")
	}
	if _, err := signer.PresignObjectDelete("user-123", key, "private", 2*time.Hour, now); err == nil {
		t.Fatal("excessive DELETE URL lifetime must be rejected")
	}
}


func TestPresignObjectPutSignsKnownOwnerScopedKey(t *testing.T) {
	signer := testSigner(t, "https://media.example.test")
	now := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	key := "users/user-123/0123456789abcdef0123456789abcdef"
	got, err := signer.PresignObjectPut("user-123", key, "public", "image/png", 10*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.Path, "/public-media/users/user-123/") {
		t.Fatalf("PUT URL has wrong bucket/path: %q", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-SignedHeaders") != "content-type;host" || q.Get("X-Amz-Expires") != "600" || len(q.Get("X-Amz-Signature")) != 64 {
		t.Fatalf("PUT URL is not correctly signed: %v", q)
	}
	if _, err := signer.PresignObjectPut("other-user", key, "public", "image/png", 10*time.Minute, now); err == nil {
		t.Fatal("cross-owner PUT URL must be rejected")
	}
}
