package virtualstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveMediaRequiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("live media replication smoke is enabled but %s is missing", name)
	}
	return value
}

func liveMediaS3Provider(t *testing.T, prefix string) *S3Provider {
	t.Helper()
	provider, err := NewS3Provider(S3Config{
		Endpoint:  liveMediaRequiredEnv(t, prefix+"_ENDPOINT"),
		Region:    liveMediaRequiredEnv(t, prefix+"_REGION"),
		AccessKey: liveMediaRequiredEnv(t, prefix+"_ACCESS_KEY_ID"),
		SecretKey: liveMediaRequiredEnv(t, prefix+"_SECRET_ACCESS_KEY"),
		Client:    &http.Client{Timeout: 30 * time.Second},
	})
	if err != nil {
		t.Fatalf("configure %s provider: %v", prefix, err)
	}
	return provider
}

func TestLiveMediaReplicationLifecycle(t *testing.T) {
	if os.Getenv("TESTAGRAM_LIVE_MEDIA_REPLICATION") != "true" {
		t.Skip("set TESTAGRAM_LIVE_MEDIA_REPLICATION=true with dedicated Instabox VPS, R2 and B2 staging buckets")
	}

	instabox := liveMediaS3Provider(t, "MEDIA_SMOKE_INSTABOX")
	r2 := liveMediaS3Provider(t, "MEDIA_SMOKE_R2")
	b2 := liveMediaS3Provider(t, "MEDIA_SMOKE_B2")

	instaboxBuckets := map[string]string{
		"media-private":           liveMediaRequiredEnv(t, "MEDIA_SMOKE_INSTABOX_PRIVATE_BUCKET"),
		"media-public":            liveMediaRequiredEnv(t, "MEDIA_SMOKE_INSTABOX_PUBLIC_BUCKET"),
		"identity-evidence-private": liveMediaRequiredEnv(t, "MEDIA_SMOKE_INSTABOX_IDENTITY_BUCKET"),
	}
	r2Buckets := map[string]string{
		"media-private":           liveMediaRequiredEnv(t, "MEDIA_SMOKE_R2_PRIVATE_BUCKET"),
		"media-public":            liveMediaRequiredEnv(t, "MEDIA_SMOKE_R2_PUBLIC_BUCKET"),
		"identity-evidence-private": liveMediaRequiredEnv(t, "MEDIA_SMOKE_R2_IDENTITY_BUCKET"),
	}
	b2Buckets := map[string]string{
		"media-private":           liveMediaRequiredEnv(t, "MEDIA_SMOKE_B2_PRIVATE_BUCKET"),
		"media-public":            liveMediaRequiredEnv(t, "MEDIA_SMOKE_B2_PUBLIC_BUCKET"),
		"identity-evidence-private": liveMediaRequiredEnv(t, "MEDIA_SMOKE_B2_IDENTITY_BUCKET"),
	}
	for name, buckets := range map[string]map[string]string{
		"Instabox VPS": instaboxBuckets, "Cloudflare R2": r2Buckets, "Backblaze B2": b2Buckets,
	} {
		seen := make(map[string]struct{}, len(buckets))
		for namespace, bucket := range buckets {
			if strings.TrimSpace(bucket) == "" {
				t.Fatalf("%s has no bucket configured for %s", name, namespace)
			}
			if _, exists := seen[bucket]; exists {
				t.Fatalf("%s reuses a physical bucket across isolated namespaces", name)
			}
			seen[bucket] = struct{}{}
		}
	}

	replicator, err := NewReplicator("instabox-vps", instabox, instaboxBuckets, []ReplicaTarget{
		{Name: "cloudflare-r2", Provider: r2, Buckets: r2Buckets},
		{Name: "backblaze-b2", Provider: b2, Buckets: b2Buckets},
	})
	if err != nil {
		t.Fatalf("configure Instabox VPS + R2 + B2 media replicator: %v", err)
	}

	tenantID := fmt.Sprintf("ci-media-%d", time.Now().UnixNano())
	cases := []struct {
		namespace   string
		contentType string
		payload     []byte
	}{
		{namespace: "media-private", contentType: "image/webp", payload: []byte("TESTAGRAM-PRIVATE-MEDIA-STAGING-CONTENT\n")},
		{namespace: "media-public", contentType: "image/webp", payload: []byte("TESTAGRAM-PUBLIC-MEDIA-STAGING-CONTENT\n")},
		{namespace: "identity-evidence-private", contentType: "application/pdf", payload: []byte("%PDF-1.7\nTESTAGRAM-IDENTITY-EVIDENCE-STAGING\n%%EOF\n")},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.namespace, func(t *testing.T) {
			objectKey := "media-staging/" + tenantID + "/" + testCase.namespace + ".bin"
			scopedKey := tenantPrefix(tenantID) + objectKey
			digestBytes := sha256.Sum256(testCase.payload)
			digest := hex.EncodeToString(digestBytes[:])
			size := int64(len(testCase.payload))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()

			buckets := map[string]string{
				"instabox-vps":   instaboxBuckets[testCase.namespace],
				"cloudflare-r2":  r2Buckets[testCase.namespace],
				"backblaze-b2":   b2Buckets[testCase.namespace],
			}
			uploaded := false
			deleted := false
			t.Cleanup(func() {
				if !uploaded || deleted {
					return
				}
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cleanupCancel()
				if err := replicator.Delete(cleanupCtx, tenantID, testCase.namespace, objectKey); err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("cleanup %s staging object: %v", testCase.namespace, err)
				}
			})

			livePutObject(t, instabox, buckets["instabox-vps"], scopedKey, testCase.contentType, testCase.payload)
			uploaded = true
			results, err := replicator.Replicate(ctx, tenantID, testCase.namespace, objectKey, size, testCase.contentType, digest)
			if err != nil {
				t.Fatalf("replicate %s through Instabox VPS, R2 and B2: %v", testCase.namespace, err)
			}
			if len(results) != 3 {
				t.Fatalf("%s verified placements=%d, want exactly 3", testCase.namespace, len(results))
			}
			providers := make(map[string]bool, len(results))
			for _, result := range results {
				if result.Size != size || !strings.EqualFold(result.SHA256, digest) {
					t.Fatalf("%s result from %s has size=%d sha256=%q; want %d/%s", testCase.namespace, result.Provider, result.Size, result.SHA256, size, digest)
				}
				providers[result.Provider] = true
			}
			for _, required := range []string{"instabox-vps", "cloudflare-r2", "backblaze-b2"} {
				if !providers[required] {
					t.Fatalf("%s replication omitted required provider %q", testCase.namespace, required)
				}
			}

			verifyAll := func(stage string) {
				t.Helper()
				checks := []struct {
					name     string
					provider *S3Provider
				}{{"Instabox VPS", instabox}, {"Cloudflare R2", r2}, {"Backblaze B2", b2}}
				for _, check := range checks {
					actual, err := check.provider.SHA256(ctx, buckets[map[string]string{"Instabox VPS": "instabox-vps", "Cloudflare R2": "cloudflare-r2", "Backblaze B2": "backblaze-b2"}[check.name]], scopedKey, size)
					if err != nil || !strings.EqualFold(actual, digest) {
						t.Fatalf("%s %s read-back SHA-256=%q err=%v; want %s", stage, check.name, actual, err, digest)
					}
				}
			}
			verifyAll("initial independent")

			// Remove the primary object and restore only from a replica with a matching streamed SHA-256.
			if err := instabox.Delete(ctx, buckets["instabox-vps"], scopedKey); err != nil {
				t.Fatalf("simulate Instabox VPS object loss: %v", err)
			}
			if _, err := instabox.Head(ctx, buckets["instabox-vps"], scopedKey); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Instabox VPS loss simulation HEAD error=%v, want not found", err)
			}
			if err := replicator.RestorePrimary(ctx, tenantID, testCase.namespace, objectKey, size, testCase.contentType, digest); err != nil {
				t.Fatalf("restore %s primary from a checksum-verified replica: %v", testCase.namespace, err)
			}
			verifyAll("after primary restore")

			// Remove R2 and require the replication path to recreate it.
			if err := r2.Delete(ctx, buckets["cloudflare-r2"], scopedKey); err != nil {
				t.Fatalf("simulate R2 loss: %v", err)
			}
			if _, err := r2.Head(ctx, buckets["cloudflare-r2"], scopedKey); !errors.Is(err, ErrNotFound) {
				t.Fatalf("R2 loss simulation HEAD error=%v, want not found", err)
			}
			if _, err := replicator.Replicate(ctx, tenantID, testCase.namespace, objectKey, size, testCase.contentType, digest); err != nil {
				t.Fatalf("repair missing R2 %s replica: %v", testCase.namespace, err)
			}
			verifyAll("after R2 repair")

			// Replace B2 with same-size corrupt bytes; SHA-256 reconciliation must repair it.
			corrupt := bytes.Repeat([]byte("X"), len(testCase.payload))
			livePutObject(t, b2, buckets["backblaze-b2"], scopedKey, testCase.contentType, corrupt)
			if _, err := replicator.Replicate(ctx, tenantID, testCase.namespace, objectKey, size, testCase.contentType, digest); err != nil {
				t.Fatalf("repair checksum-drifted B2 %s replica: %v", testCase.namespace, err)
			}
			verifyAll("after B2 checksum repair")

			if err := replicator.Delete(ctx, tenantID, testCase.namespace, objectKey); err != nil {
				t.Fatalf("delete %s from all three providers: %v", testCase.namespace, err)
			}
			deleted = true
			for _, check := range []struct {
				name     string
				provider *S3Provider
				bucket   string
			}{{"Instabox VPS", instabox, buckets["instabox-vps"]}, {"Cloudflare R2", r2, buckets["cloudflare-r2"]}, {"Backblaze B2", b2, buckets["backblaze-b2"]}} {
				if _, err := check.provider.Head(ctx, check.bucket, scopedKey); !errors.Is(err, ErrNotFound) {
					t.Fatalf("%s still exposes %s after deletion: %v", check.name, testCase.namespace, err)
				}
			}
		})
	}
}
