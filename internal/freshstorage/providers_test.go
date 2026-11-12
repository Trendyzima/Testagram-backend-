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
	for _, k := range []string{"B2_S3_ENDPOINT", "B2_S3_REGION", "B2_S3_ACCESS_KEY_ID", "B2_S3_SECRET_ACCESS_KEY", "B2_S3_BUCKET", "CLOUDFLARE_R2_ENDPOINT", "CLOUDFLARE_R2_REGION", "CLOUDFLARE_R2_ACCESS_KEY_ID", "CLOUDFLARE_R2_SECRET_ACCESS_KEY", "CLOUDFLARE_R2_BUCKET", "ARCHIVE_S3_ENDPOINT", "ARCHIVE_S3_REGION", "ARCHIVE_S3_ACCESS_KEY_ID", "ARCHIVE_S3_SECRET_ACCESS_KEY", "ARCHIVE_S3_BUCKET", "CLOUDFLARE_MEDIA_BASE_URL", "FRESH_MEDIA_PRIMARY_PROVIDER", "FRESH_MEDIA_BACKEND_URL", "FRESH_MEDIA_INTERNAL_TOKEN"} {
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

func TestBackblazeMissingRegionFailsClosed(t *testing.T) {
	clear(t)
	t.Setenv("B2_S3_ENDPOINT", "https://s3.us-east-005.backblazeb2.com")
	t.Setenv("B2_S3_ACCESS_KEY_ID", "test-key")
	t.Setenv("B2_S3_SECRET_ACCESS_KEY", strings.Repeat("s", 40))
	t.Setenv("B2_S3_BUCKET", "testagram-private-media")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "B2_S3_REGION") {
		t.Fatalf("missing B2 region must fail closed, err=%v", err)
	}
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

func TestFromEnvRequiresBackendAssertionBridgeForCloudflareCDN(t *testing.T) {
	clear(t)
	t.Setenv("CLOUDFLARE_MEDIA_BASE_URL", "https://media.testagram.example")
	if _, err := FromEnv(); err == nil {
		t.Fatal("Cloudflare CDN configured without Backend assertion bridge")
	}
	t.Setenv("FRESH_MEDIA_BACKEND_URL", "https://api.testagram.example")
	t.Setenv("FRESH_MEDIA_INTERNAL_TOKEN", strings.Repeat("x", 32))
	p, err := FromEnv()
	if err != nil { t.Fatalf("valid Backend assertion bridge rejected: %v", err) }
	if p.BackendAPIBaseURL != "https://api.testagram.example" || p.BackendInternalToken == "" {
		t.Fatalf("Backend bridge configuration missing: %+v", p)
	}
	t.Setenv("FRESH_MEDIA_BACKEND_URL", "http://api.testagram.example")
	if _, err := FromEnv(); err == nil { t.Fatal("remote HTTP Backend bridge URL accepted") }
}

func TestFromEnvRequiresBackendBridgeURLAndTokenTogether(t *testing.T) {
	clear(t)
	t.Setenv("FRESH_MEDIA_BACKEND_URL", "https://api.testagram.example")
	if _, err := FromEnv(); err == nil { t.Fatal("Backend URL without internal token accepted") }
	t.Setenv("FRESH_MEDIA_BACKEND_URL", "")
	t.Setenv("FRESH_MEDIA_INTERNAL_TOKEN", strings.Repeat("x",32))
	if _, err := FromEnv(); err == nil { t.Fatal("internal token without Backend URL accepted") }
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

func TestBackblazeRequiresExplicitRegionAndSignsWithBucketRegion(t *testing.T) {
	clear(t)
	t.Setenv("B2_S3_ENDPOINT", "https://s3.us-east-005.backblazeb2.com")
	t.Setenv("B2_S3_ACCESS_KEY_ID", "test-b2-key-id")
	t.Setenv("B2_S3_SECRET_ACCESS_KEY", strings.Repeat("s", 40))
	t.Setenv("B2_S3_BUCKET", "testagram-private-media")
	t.Setenv("B2_S3_REGION", "us-east-005")
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
