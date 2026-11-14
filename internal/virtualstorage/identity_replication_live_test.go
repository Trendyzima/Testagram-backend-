package virtualstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveRequiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("live identity replication smoke is enabled but %s is missing", name)
	}
	return value
}

func liveS3Provider(t *testing.T, prefix string) *S3Provider {
	t.Helper()
	provider, err := NewS3Provider(S3Config{
		Endpoint: liveRequiredEnv(t, prefix+"_ENDPOINT"),
		Region: liveRequiredEnv(t, prefix+"_REGION"),
		AccessKey: liveRequiredEnv(t, prefix+"_ACCESS_KEY_ID"),
		SecretKey: liveRequiredEnv(t, prefix+"_SECRET_ACCESS_KEY"),
		Client: &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		t.Fatalf("configure %s provider: %v", prefix, err)
	}
	return provider
}

func livePutObject(t *testing.T, provider *S3Provider, bucket, key, contentType string, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	putURL, err := provider.PresignPut(ctx, bucket, key, contentType, int64(len(payload)), 5*time.Minute)
	if err != nil {
		t.Fatalf("presign %s object: %v", bucket, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build %s upload request: %v", bucket, err)
	}
	request.Header.Set("Content-Type", contentType)
	request.ContentLength = int64(len(payload))
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("upload to %s: %v", bucket, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		t.Fatalf("upload to %s returned HTTP %d: %s", bucket, response.StatusCode, strings.TrimSpace(string(detail)))
	}
}

func TestLiveIdentityEvidenceReplicationLifecycle(t *testing.T) {
	if os.Getenv("TESTAGRAM_LIVE_IDENTITY_EVIDENCE_REPLICATION") != "true" {
		t.Skip("set TESTAGRAM_LIVE_IDENTITY_EVIDENCE_REPLICATION=true with isolated staging MinIO, B2 and R2 buckets")
	}

	primary := liveS3Provider(t, "IDENTITY_SMOKE_PRIMARY")
	r2 := liveS3Provider(t, "IDENTITY_SMOKE_R2")
	b2 := liveS3Provider(t, "IDENTITY_SMOKE_B2")

	primaryBuckets := map[string]string{
		"media-private": liveRequiredEnv(t, "IDENTITY_SMOKE_PRIMARY_PRIVATE_BUCKET"),
		"media-public": liveRequiredEnv(t, "IDENTITY_SMOKE_PRIMARY_PUBLIC_BUCKET"),
		"identity-evidence-private": liveRequiredEnv(t, "IDENTITY_SMOKE_PRIMARY_IDENTITY_BUCKET"),
	}
	r2Buckets := map[string]string{
		"media-private": liveRequiredEnv(t, "IDENTITY_SMOKE_R2_PRIVATE_BUCKET"),
		"media-public": liveRequiredEnv(t, "IDENTITY_SMOKE_R2_PUBLIC_BUCKET"),
		"identity-evidence-private": liveRequiredEnv(t, "IDENTITY_SMOKE_R2_IDENTITY_BUCKET"),
	}
	b2Buckets := map[string]string{
		"media-private": liveRequiredEnv(t, "IDENTITY_SMOKE_B2_PRIVATE_BUCKET"),
		"media-public": liveRequiredEnv(t, "IDENTITY_SMOKE_B2_PUBLIC_BUCKET"),
		"identity-evidence-private": liveRequiredEnv(t, "IDENTITY_SMOKE_B2_IDENTITY_BUCKET"),
	}
	for name, buckets := range map[string]map[string]string{
		"primary": primaryBuckets, "r2": r2Buckets, "b2": b2Buckets,
	} {
		if buckets["media-private"] == "" || buckets["media-public"] == "" ||
			buckets["media-private"] == buckets["media-public"] ||
			buckets["identity-evidence-private"] == "" ||
			buckets["identity-evidence-private"] == buckets["media-private"] ||
			buckets["identity-evidence-private"] == buckets["media-public"] {
			t.Fatalf("%s bucket mapping violates private/public/identity isolation", name)
		}
	}

	replicator, err := NewReplicator("instabox-minio-smoke", primary, primaryBuckets, []ReplicaTarget{
		{Name: "cloudflare-r2", Provider: r2, Buckets: r2Buckets},
		{Name: "backblaze-b2", Provider: b2, Buckets: b2Buckets},
	})
	if err != nil {
		t.Fatalf("configure three-provider identity evidence replicator: %v", err)
	}

	tenantID := fmt.Sprintf("ci-identity-%d", time.Now().UnixNano())
	objectKey := "identity-evidence-smoke/" + tenantID + "/evidence.pdf"
	scopedKey := tenantPrefix(tenantID) + objectKey
	payload := []byte("%PDF-1.7\nTestagram identity evidence replication staging smoke\n%%EOF\n")
	digestBytes := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestBytes[:])
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cleanupCancel()
		if err := replicator.Delete(cleanupCtx, tenantID, "identity-evidence-private", objectKey); err != nil {
			t.Errorf("cleanup staging identity evidence object: %v", err)
		}
	})

	livePutObject(t, primary, primaryBuckets["identity-evidence-private"], scopedKey, "application/pdf", payload)
	results, err := replicator.Replicate(ctx, tenantID, "identity-evidence-private", objectKey, int64(len(payload)), "application/pdf", digest)
	if err != nil {
		t.Fatalf("replicate identity evidence to B2 and R2: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("verified provider placements=%d, want 3", len(results))
	}

	for _, target := range []struct {
		name string
		provider *S3Provider
		bucket string
	}{{"R2", r2, r2Buckets["identity-evidence-private"]}, {"B2", b2, b2Buckets["identity-evidence-private"]}} {
		actual, err := target.provider.SHA256(ctx, target.bucket, scopedKey, int64(len(payload)))
		if err != nil || actual != digest {
			t.Fatalf("%s identity evidence checksum=%q, err=%v; want %s", target.name, actual, err, digest)
		}
	}

	// Simulate primary loss and restore it only from a checksum-verified replica.
	if err := primary.Delete(ctx, primaryBuckets["identity-evidence-private"], scopedKey); err != nil {
		t.Fatalf("simulate primary object loss: %v", err)
	}
	if _, err := primary.Head(ctx, primaryBuckets["identity-evidence-private"], scopedKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("primary loss simulation HEAD error=%v, want not found", err)
	}
	if err := replicator.RestorePrimary(ctx, tenantID, "identity-evidence-private", objectKey, int64(len(payload)), "application/pdf", digest); err != nil {
		t.Fatalf("restore missing primary identity evidence from verified replica: %v", err)
	}
	if actual, err := primary.SHA256(ctx, primaryBuckets["identity-evidence-private"], scopedKey, int64(len(payload))); err != nil || actual != digest {
		t.Fatalf("restored primary checksum=%q err=%v, want %s", actual, err, digest)
	}

	// Simulate loss of one replica and require the same Replicate path to restore it.
	if err := r2.Delete(ctx, r2Buckets["identity-evidence-private"], scopedKey); err != nil {
		t.Fatalf("simulate R2 replica loss: %v", err)
	}
	if _, err := r2.Head(ctx, r2Buckets["identity-evidence-private"], scopedKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("R2 loss simulation HEAD error=%v, want not found", err)
	}
	if _, err := replicator.Replicate(ctx, tenantID, "identity-evidence-private", objectKey, int64(len(payload)), "application/pdf", digest); err != nil {
		t.Fatalf("restore missing R2 identity evidence replica: %v", err)
	}

	// Replace B2 with a same-size but incorrect object; reconciliation must repair it.
	corrupt := []byte("%PDF-1.7\nCorrupt identity evidence bytes!\n%%EOF\n")
	if len(corrupt) != len(payload) {
		corrupt = append(corrupt[:0], bytes.Repeat([]byte("X"), len(payload))...)
	}
	livePutObject(t, b2, b2Buckets["identity-evidence-private"], scopedKey, "application/pdf", corrupt)
	if _, err := replicator.Replicate(ctx, tenantID, "identity-evidence-private", objectKey, int64(len(payload)), "application/pdf", digest); err != nil {
		t.Fatalf("repair checksum-drifted B2 identity evidence replica: %v", err)
	}
	b2Digest, err := b2.SHA256(ctx, b2Buckets["identity-evidence-private"], scopedKey, int64(len(payload)))
	if err != nil || b2Digest != digest {
		t.Fatalf("B2 identity evidence checksum after repair=%q, err=%v; want %s", b2Digest, err, digest)
	}

	if err := replicator.Delete(ctx, tenantID, "identity-evidence-private", objectKey); err != nil {
		t.Fatalf("delete identity evidence from all three providers: %v", err)
	}
	for _, target := range []struct {
		name string
		provider *S3Provider
		bucket string
	}{{"MinIO primary", primary, primaryBuckets["identity-evidence-private"]}, {"R2", r2, r2Buckets["identity-evidence-private"]}, {"B2", b2, b2Buckets["identity-evidence-private"]}} {
		if _, err := target.provider.Head(ctx, target.bucket, scopedKey); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s still exposes identity evidence after delete: %v", target.name, err)
		}
	}
}
