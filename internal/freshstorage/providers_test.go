package freshstorage

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func clear(t *testing.T) {
	t.Helper()
	for _, k := range []string{"B2_S3_ENDPOINT", "B2_S3_REGION", "B2_S3_ACCESS_KEY_ID", "B2_S3_SECRET_ACCESS_KEY", "B2_S3_BUCKET", "CLOUDFLARE_R2_ENDPOINT", "CLOUDFLARE_R2_REGION", "CLOUDFLARE_R2_ACCESS_KEY_ID", "CLOUDFLARE_R2_SECRET_ACCESS_KEY", "CLOUDFLARE_R2_BUCKET", "ARCHIVE_S3_ENDPOINT", "ARCHIVE_S3_REGION", "ARCHIVE_S3_ACCESS_KEY_ID", "ARCHIVE_S3_SECRET_ACCESS_KEY", "ARCHIVE_S3_BUCKET", "CLOUDFLARE_MEDIA_BASE_URL", "CLOUDFLARE_MEDIA_SIGNING_SECRET", "FRESH_MEDIA_PRIMARY_PROVIDER"} {
		old, ok := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

func TestEmptyConfigurationIsNotReady(t *testing.T) {
	clear(t)
	p, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ReadyForMedia(); err == nil {
		t.Fatal("expected B2 and R2 to be required before media launch")
	}
}

func TestPartialB2ConfigurationFailsClosed(t *testing.T) {
	clear(t)
	_ = os.Setenv("B2_S3_ENDPOINT", "https://s3.us-east-005.backblazeb2.com")
	p, err := FromEnv()
	if err == nil || p.B2 != nil {
		t.Fatal("partial B2 configuration must fail closed")
	}
}

func TestR2CannotLaunchMediaWithoutB2(t *testing.T) {
	clear(t)
	_ = os.Setenv("CLOUDFLARE_R2_ENDPOINT", "https://account.r2.cloudflarestorage.com")
	_ = os.Setenv("CLOUDFLARE_R2_ACCESS_KEY_ID", "key")
	_ = os.Setenv("CLOUDFLARE_R2_SECRET_ACCESS_KEY", "secret")
	_ = os.Setenv("CLOUDFLARE_R2_BUCKET", "media")
	p, err := FromEnv()
	if err != nil || p.R2 == nil {
		t.Fatalf("R2 should configure independently; media readiness still requires B2: %v", err)
	}
	if err := p.ReadyForMedia(); err == nil {
		t.Fatal("media must remain unavailable until both independent providers are configured")
	}
}


func TestR2CanBeSelectedAsPrimaryWithBothProviders(t *testing.T) {
	clear(t)
	for key, value := range map[string]string{
		"B2_S3_ENDPOINT": "https://s3.us-east-005.backblazeb2.com",
		"B2_S3_ACCESS_KEY_ID": "b2-key",
		"B2_S3_SECRET_ACCESS_KEY": "b2-secret",
		"B2_S3_BUCKET": "testagram-b2",
		"CLOUDFLARE_R2_ENDPOINT": "https://account.r2.cloudflarestorage.com",
		"CLOUDFLARE_R2_ACCESS_KEY_ID": "r2-key",
		"CLOUDFLARE_R2_SECRET_ACCESS_KEY": "r2-secret",
		"CLOUDFLARE_R2_BUCKET": "testagram-r2",
		"FRESH_MEDIA_PRIMARY_PROVIDER": "r2",
	} { t.Setenv(key, value) }
	p, err := FromEnv()
	if err != nil { t.Fatal(err) }
	if err := p.ReadyForMedia(); err != nil { t.Fatal(err) }
	if p.PrimaryProvider != "r2" { t.Fatalf("primary provider=%q, want r2", p.PrimaryProvider) }
}

func TestInvalidPrimaryProviderFailsClosed(t *testing.T) {
	clear(t)
	t.Setenv("FRESH_MEDIA_PRIMARY_PROVIDER", "third-party")
	if _, err := FromEnv(); err == nil { t.Fatal("unsupported primary provider accepted") }
}

func TestTargetPoolIs150GBDecimal(t *testing.T) {
	if TargetPoolBytes != 150_000_000_000 {
		t.Fatalf("TargetPoolBytes = %d, want 150,000,000,000", TargetPoolBytes)
	}
}

func TestFromEnvValidatesPrivateCloudflareMediaSigningPair(t *testing.T) {
	t.Setenv("B2_S3_ENDPOINT", "")
	t.Setenv("B2_S3_ACCESS_KEY_ID", "")
	t.Setenv("B2_S3_SECRET_ACCESS_KEY", "")
	t.Setenv("B2_S3_BUCKET", "")
	t.Setenv("CLOUDFLARE_R2_ENDPOINT", "")
	t.Setenv("CLOUDFLARE_R2_ACCESS_KEY_ID", "")
	t.Setenv("CLOUDFLARE_R2_SECRET_ACCESS_KEY", "")
	t.Setenv("CLOUDFLARE_R2_BUCKET", "")
	t.Setenv("CLOUDFLARE_MEDIA_BASE_URL", "https://media.testagram.example")
	t.Setenv("CLOUDFLARE_MEDIA_SIGNING_SECRET", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("incomplete Cloudflare URL/signing-secret pair was accepted")
	}
	t.Setenv("CLOUDFLARE_MEDIA_SIGNING_SECRET", strings.Repeat("x", 32))
	if _, err := FromEnv(); err != nil {
		t.Fatalf("valid private CDN configuration rejected: %v", err)
	}
	t.Setenv("CLOUDFLARE_MEDIA_BASE_URL", "http://media.testagram.example")
	if _, err := FromEnv(); err == nil {
		t.Fatal("insecure Cloudflare media origin was accepted")
	}
}

func TestArchiveProviderRequiresCompleteIndependentConfiguration(t *testing.T) {
	clear(t)
	_ = os.Setenv("ARCHIVE_S3_ENDPOINT", "https://archive.example.test")
	if _, err := FromEnv(); err == nil {
		t.Fatal("partial archive provider configuration was accepted")
	}
	_ = os.Setenv("ARCHIVE_S3_ACCESS_KEY_ID", "archive-key")
	_ = os.Setenv("ARCHIVE_S3_SECRET_ACCESS_KEY", "archive-secret")
	_ = os.Setenv("ARCHIVE_S3_BUCKET", "cold-archive")
	p, err := FromEnv()
	if err != nil {
		t.Fatalf("complete archive provider configuration rejected: %v", err)
	}
	if p.Archive == nil || p.ArchiveBucket != "cold-archive" {
		t.Fatalf("archive provider not configured: %#v", p)
	}
}

func TestBackblazeDefaultRegionMatchesUsEast005Endpoint(t *testing.T) {
	clear(t)
	t.Setenv("B2_S3_ENDPOINT", "https://s3.us-east-005.backblazeb2.com")
	t.Setenv("B2_S3_ACCESS_KEY_ID", "test-b2-key-id")
	t.Setenv("B2_S3_SECRET_ACCESS_KEY", strings.Repeat("s", 40))
	t.Setenv("B2_S3_BUCKET", "testagram-private-media")
	t.Setenv("B2_S3_REGION", "")
	p, err := FromEnv()
	if err != nil {
		t.Fatalf("configure B2: %v", err)
	}
	if p.B2 == nil || p.B2Bucket != "testagram-private-media" {
		t.Fatalf("B2 provider not configured: %#v", p)
	}
	putURL, err := p.B2.PresignPut(context.Background(), p.B2Bucket, "v1/0123456789abcdef0123456789abcdef", "image/jpeg", 1024, time.Minute)
	if err != nil {
		t.Fatalf("presign B2 upload: %v", err)
	}
	parsed, err := url.Parse(putURL)
	if err != nil {
		t.Fatal(err)
	}
	credential := parsed.Query().Get("X-Amz-Credential")
	if !strings.Contains(credential, "/us-east-005/s3/aws4_request") {
		t.Fatalf("B2 signing credential uses wrong region: %q", credential)
	}
}
