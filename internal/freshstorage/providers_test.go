package freshstorage

import (
	"os"
	"strings"
	"testing"
)

func clear(t *testing.T) {
	t.Helper()
	for _, k := range []string{"B2_S3_ENDPOINT", "B2_S3_REGION", "B2_S3_ACCESS_KEY_ID", "B2_S3_SECRET_ACCESS_KEY", "B2_S3_BUCKET", "CLOUDFLARE_R2_ENDPOINT", "CLOUDFLARE_R2_REGION", "CLOUDFLARE_R2_ACCESS_KEY_ID", "CLOUDFLARE_R2_SECRET_ACCESS_KEY", "CLOUDFLARE_R2_BUCKET", "ARCHIVE_S3_ENDPOINT", "ARCHIVE_S3_REGION", "ARCHIVE_S3_ACCESS_KEY_ID", "ARCHIVE_S3_SECRET_ACCESS_KEY", "ARCHIVE_S3_BUCKET"} {
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
	_ = os.Setenv("B2_S3_ENDPOINT", "https://s3.us-west-004.backblazeb2.com")
	p, err := FromEnv()
	if err == nil || p.B2 != nil {
		t.Fatal("partial B2 configuration must fail closed")
	}
}

func TestR2CannotBeConfiguredWithoutB2(t *testing.T) {
	clear(t)
	_ = os.Setenv("CLOUDFLARE_R2_ENDPOINT", "https://account.r2.cloudflarestorage.com")
	_ = os.Setenv("CLOUDFLARE_R2_ACCESS_KEY_ID", "key")
	_ = os.Setenv("CLOUDFLARE_R2_SECRET_ACCESS_KEY", "secret")
	_ = os.Setenv("CLOUDFLARE_R2_BUCKET", "media")
	p, err := FromEnv()
	if err == nil || p.R2 != nil {
		t.Fatal("R2 without B2 primary must fail closed")
	}
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
