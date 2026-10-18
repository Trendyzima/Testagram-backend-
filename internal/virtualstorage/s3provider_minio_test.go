package virtualstorage

import (
	"bytes"
	"context"
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
