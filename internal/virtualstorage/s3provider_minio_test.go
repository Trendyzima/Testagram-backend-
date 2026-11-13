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
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This test runs in the dedicated CI job against a real MinIO service. It skips
// in ordinary unit-test runs unless TESTAGRAM_S3_ENDPOINT is configured.
func TestS3ProviderMinIOLifecycle(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("TESTAGRAM_S3_ENDPOINT"))
	if endpoint == "" {
		t.Skip("TESTAGRAM_S3_ENDPOINT is not configured")
	}
	accessKey := os.Getenv("TESTAGRAM_S3_ACCESS_KEY")
	secretKey := os.Getenv("TESTAGRAM_S3_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		t.Fatal("TESTAGRAM_S3_ACCESS_KEY and TESTAGRAM_S3_SECRET_KEY are required with TESTAGRAM_S3_ENDPOINT")
	}

	p, err := NewS3Provider(S3Config{
		Endpoint: endpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Region: "us-east-1",
		Client: &http.Client{Timeout: 8 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bucket := "testagram-vstorage-ci"
	key := fmt.Sprintf("tenants/ci/object-%d.txt", time.Now().UnixNano())
	payload := []byte("virtual-storage-minio-roundtrip")

	createURL, err := p.presign(ctx, http.MethodPut, bucket, "", url.Values{}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.do(mustRequest(t, ctx, http.MethodPut, createURL, nil))
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	drainAndClose(t, resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("create bucket returned HTTP %d", resp.StatusCode)
	}
	defer func() {
		deleteURL, signErr := p.presign(context.Background(), http.MethodDelete, bucket, "", url.Values{}, nil, time.Minute)
		if signErr == nil {
			if req, reqErr := http.NewRequest(http.MethodDelete, deleteURL, nil); reqErr == nil {
				if cleanupResp, doErr := p.do(req); doErr == nil {
					_ = cleanupResp.Body.Close()
				}
			}
		}
	}()

	putURL, err := p.PresignPut(ctx, bucket, key, "text/plain", int64(len(payload)), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	putReq := mustRequest(t, ctx, http.MethodPut, putURL, bytes.NewReader(payload))
	putReq.Header.Set("Content-Type", "text/plain")
	resp, err = p.do(putReq)
	if err != nil {
		t.Fatalf("upload object: %v", err)
	}
	drainAndClose(t, resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("upload object returned HTTP %d", resp.StatusCode)
	}

	info, err := p.Head(ctx, bucket, key)
	if err != nil {
		t.Fatalf("HEAD uploaded object: %v", err)
	}
	if info.Size != int64(len(payload)) || info.ContentType != "text/plain" {
		t.Fatalf("HEAD metadata mismatch: %+v", info)
	}

	getURL, err := p.PresignGet(ctx, bucket, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = p.do(mustRequest(t, ctx, http.MethodGet, getURL, nil))
	if err != nil {
		t.Fatalf("GET uploaded object: %v", err)
	}
	got, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	drainAndClose(t, resp)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, payload) {
		t.Fatalf("GET mismatch: status=%d body=%q", resp.StatusCode, got)
	}

	items, cursor, err := p.List(ctx, bucket, "tenants/ci/", "", 100)
	if err != nil {
		t.Fatalf("LIST uploaded object: %v", err)
	}
	if cursor != "" || len(items) != 1 || items[0].Key != key || items[0].Size != int64(len(payload)) {
		t.Fatalf("LIST mismatch: items=%+v cursor=%q", items, cursor)
	}

	if err := p.Delete(ctx, bucket, key); err != nil {
		t.Fatalf("DELETE uploaded object: %v", err)
	}
	if _, err := p.Head(ctx, bucket, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("HEAD after delete = %v, want ErrNotFound", err)
	}
}

func mustRequest(t *testing.T, ctx context.Context, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func drainAndClose(t *testing.T, resp *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); err != nil {
		t.Errorf("drain response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response: %v", err)
	}
}


func TestReplicatorMinIOThreeProviderLifecycle(t *testing.T) {
	endpoints := []string{
		strings.TrimSpace(os.Getenv("TESTAGRAM_REPLICA_PRIMARY_ENDPOINT")),
		strings.TrimSpace(os.Getenv("TESTAGRAM_REPLICA_R2_ENDPOINT")),
		strings.TrimSpace(os.Getenv("TESTAGRAM_REPLICA_B2_ENDPOINT")),
	}
	for _, endpoint := range endpoints {
		if endpoint == "" {
			t.Skip("three TESTAGRAM_REPLICA_*_ENDPOINT values are required")
		}
	}
	accessKey := os.Getenv("TESTAGRAM_S3_ACCESS_KEY")
	secretKey := os.Getenv("TESTAGRAM_S3_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		t.Fatal("MinIO replica integration credentials are required")
	}
	providers := make([]*S3Provider, 0, 3)
	for _, endpoint := range endpoints {
		provider, err := NewS3Provider(S3Config{
			Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey,
			Region: "us-east-1", Client: &http.Client{Timeout: 15 * time.Second},
		})
		if err != nil { t.Fatal(err) }
		providers = append(providers, provider)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	buckets := []string{"testagram-replica-primary", "testagram-replica-r2", "testagram-replica-b2"}
	for i, provider := range providers {
		createMinIOTestBucket(t, ctx, provider, buckets[i])
	}
	key := fmt.Sprintf("users/user-1/replica-%d.bin", time.Now().UnixNano())
	scopedKey := tenantPrefix("user-1") + key
	payload := bytes.Repeat([]byte("testagram-three-provider-replication-"), 64*1024)
	digestBytes := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestBytes[:])

	putURL, err := providers[0].PresignPut(ctx, buckets[0], scopedKey, "application/octet-stream", int64(len(payload)), 5*time.Minute)
	if err != nil { t.Fatal(err) }
	putReq := mustRequest(t, ctx, http.MethodPut, putURL, bytes.NewReader(payload))
	putReq.Header.Set("Content-Type", "application/octet-stream")
	resp, err := providers[0].do(putReq)
	if err != nil { t.Fatal(err) }
	drainAndClose(t, resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { t.Fatalf("primary upload status=%d", resp.StatusCode) }

	replicator, err := NewReplicator("instabox-vps", providers[0],
		map[string]string{"media-private":buckets[0],"media-public":buckets[0],"identity-evidence-private":buckets[0]},
		[]ReplicaTarget{
			{Name:"cloudflare-r2",Provider:providers[1],Buckets:map[string]string{"media-private":buckets[1],"media-public":buckets[1],"identity-evidence-private":buckets[1]}},
			{Name:"backblaze-b2",Provider:providers[2],Buckets:map[string]string{"media-private":buckets[2],"media-public":buckets[2],"identity-evidence-private":buckets[2]}},
		})
	if err != nil { t.Fatal(err) }
	results, err := replicator.Replicate(ctx,"user-1","media-private",key,int64(len(payload)),"application/octet-stream",digest)
	if err != nil { t.Fatalf("replicate to independent providers: %v",err) }
	if len(results)!=3 { t.Fatalf("replication results=%d, want 3",len(results)) }
	for i, provider := range providers {
		info, err := provider.Head(ctx,buckets[i],scopedKey)
		if err != nil { t.Fatalf("provider %d HEAD: %v",i,err) }
		if info.Size!=int64(len(payload)) { t.Fatalf("provider %d size=%d",i,info.Size) }
		got, err := provider.SHA256(ctx,buckets[i],scopedKey,int64(len(payload)))
		if err != nil || got!=digest { t.Fatalf("provider %d checksum=%q err=%v",i,got,err) }
	}

	if err:=replicator.Delete(ctx,"user-1","media-private",key);err!=nil { t.Fatalf("delete replicas: %v",err) }
	for i,provider:=range providers {
		if _,err:=provider.Head(ctx,buckets[i],scopedKey);!errors.Is(err,ErrNotFound) {
			t.Fatalf("provider %d HEAD after delete=%v, want ErrNotFound",i,err)
		}
	}

	// Exercise the dedicated identity-evidence namespace across all three
	// providers, including independent checksum read-back and deletion.
	identityKey := "identity/session-ci/evidence.pdf"
	identityScopedKey := tenantPrefix("application-ci") + identityKey
	identityPayload := []byte("testagram-private-identity-evidence-fixture")
	identityDigestBytes := sha256.Sum256(identityPayload)
	identityDigest := hex.EncodeToString(identityDigestBytes[:])
	identityPutURL, err := providers[0].PresignPut(ctx, buckets[0], identityScopedKey, "application/pdf", int64(len(identityPayload)), 5*time.Minute)
	if err != nil { t.Fatal(err) }
	identityPutReq := mustRequest(t, ctx, http.MethodPut, identityPutURL, bytes.NewReader(identityPayload))
	identityPutReq.Header.Set("Content-Type", "application/pdf")
	resp, err = providers[0].do(identityPutReq)
	if err != nil { t.Fatal(err) }
	drainAndClose(t, resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { t.Fatalf("identity evidence primary upload status=%d", resp.StatusCode) }
	identityResults, err := replicator.Replicate(ctx, "application-ci", "identity-evidence-private", identityKey, int64(len(identityPayload)), "application/pdf", identityDigest)
	if err != nil { t.Fatalf("replicate identity evidence: %v", err) }
	if len(identityResults) != 3 { t.Fatalf("identity evidence verified placements=%d, want 3", len(identityResults)) }
	for i, provider := range providers {
		info, err := provider.Head(ctx, buckets[i], identityScopedKey)
		if err != nil || info.Size != int64(len(identityPayload)) || info.ContentType != "application/pdf" {
			t.Fatalf("identity evidence provider %d metadata=%+v err=%v", i, info, err)
		}
		got, err := provider.SHA256(ctx, buckets[i], identityScopedKey, int64(len(identityPayload)))
		if err != nil || got != identityDigest { t.Fatalf("identity evidence provider %d checksum=%q err=%v", i, got, err) }
	}
	if err := replicator.Delete(ctx, "application-ci", "identity-evidence-private", identityKey); err != nil {
		t.Fatalf("delete identity evidence replicas: %v", err)
	}
	for i, provider := range providers {
		if _, err := provider.Head(ctx, buckets[i], identityScopedKey); !errors.Is(err, ErrNotFound) {
			t.Fatalf("identity evidence provider %d HEAD after delete=%v, want ErrNotFound", i, err)
		}
	}
}

func createMinIOTestBucket(t *testing.T, ctx context.Context, provider *S3Provider, bucket string) {
	t.Helper()
	createURL, err := provider.presign(ctx,http.MethodPut,bucket,"",url.Values{},nil,time.Minute)
	if err != nil { t.Fatal(err) }
	resp, err := provider.do(mustRequest(t,ctx,http.MethodPut,createURL,nil))
	if err != nil { t.Fatalf("create bucket %s: %v",bucket,err) }
	drainAndClose(t,resp)
	if resp.StatusCode<200 || resp.StatusCode>=300 { t.Fatalf("create bucket %s status=%d",bucket,resp.StatusCode) }
}
