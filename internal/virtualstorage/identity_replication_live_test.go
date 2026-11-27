package virtualstorage

import (
	"bytes"
	"context"
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

