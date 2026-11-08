package freshstorage

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

const TargetPoolBytes int64 = 150_000_000_000

type ProviderSet struct {
	B2                    virtualstorage.Provider
	B2Bucket              string
	R2                    virtualstorage.Provider
	R2Bucket              string
	Archive               virtualstorage.Provider
	ArchiveBucket         string
	MediaCDNBaseURL       string
	MediaCDNSigningSecret string
}

func FromEnv() (ProviderSet, error) {
	var out ProviderSet
	var err error
	out.B2, out.B2Bucket, err = providerFromEnv("B2_S3_ENDPOINT", "B2_S3_REGION", "B2_S3_ACCESS_KEY_ID", "B2_S3_SECRET_ACCESS_KEY", "B2_S3_BUCKET", "us-east-005")
	if err != nil {
		return ProviderSet{}, fmt.Errorf("Backblaze B2: %w", err)
	}
	out.R2, out.R2Bucket, err = providerFromEnv("CLOUDFLARE_R2_ENDPOINT", "CLOUDFLARE_R2_REGION", "CLOUDFLARE_R2_ACCESS_KEY_ID", "CLOUDFLARE_R2_SECRET_ACCESS_KEY", "CLOUDFLARE_R2_BUCKET", "auto")
	if err != nil {
		return ProviderSet{}, fmt.Errorf("Cloudflare R2: %w", err)
	}
	out.Archive, out.ArchiveBucket, err = providerFromEnv("ARCHIVE_S3_ENDPOINT", "ARCHIVE_S3_REGION", "ARCHIVE_S3_ACCESS_KEY_ID", "ARCHIVE_S3_SECRET_ACCESS_KEY", "ARCHIVE_S3_BUCKET", "us-east-1")
	if err != nil {
		return ProviderSet{}, fmt.Errorf("archive provider: %w", err)
	}
	out.MediaCDNBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("CLOUDFLARE_MEDIA_BASE_URL")), "/")
	out.MediaCDNSigningSecret = strings.TrimSpace(os.Getenv("CLOUDFLARE_MEDIA_SIGNING_SECRET"))
	if (out.MediaCDNBaseURL == "") != (out.MediaCDNSigningSecret == "") {
		return ProviderSet{}, errors.New("Cloudflare media CDN base URL and signing secret must be configured together")
	}
	if out.MediaCDNBaseURL != "" {
		if err := validateMediaCDNBaseURL(out.MediaCDNBaseURL); err != nil {
			return ProviderSet{}, err
		}
		if len(out.MediaCDNSigningSecret) < 32 {
			return ProviderSet{}, errors.New("Cloudflare media CDN signing secret must be at least 32 characters")
		}
	}
	if out.R2 != nil && out.B2 == nil {
		return ProviderSet{}, errors.New("Cloudflare R2 cannot be configured as a failover-only store: configure Backblaze B2 as the primary durable origin")
	}
	return out, nil
}

func (p ProviderSet) ReadyForMedia() error {
	if p.B2 == nil || p.B2Bucket == "" {
		return errors.New("Backblaze B2 primary storage is not configured")
	}
	if p.R2 == nil || p.R2Bucket == "" {
		return errors.New("Cloudflare R2 additional storage is not configured")
	}
	return nil
}

func providerFromEnv(endpointName, regionName, keyName, secretName, bucketName, defaultRegion string) (virtualstorage.Provider, string, error) {
	values := []string{strings.TrimSpace(os.Getenv(endpointName)), strings.TrimSpace(os.Getenv(keyName)), strings.TrimSpace(os.Getenv(secretName)), strings.TrimSpace(os.Getenv(bucketName))}
	configured := 0
	for _, v := range values {
		if v != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil, "", nil
	}
	if configured != len(values) {
		return nil, "", fmt.Errorf("%s, access key, secret key and bucket must be configured together", endpointName)
	}
	region := strings.TrimSpace(os.Getenv(regionName))
	if region == "" {
		region = defaultRegion
	}
	provider, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{Endpoint: values[0], Region: region, AccessKey: values[1], SecretKey: values[2]})
	if err != nil {
		return nil, "", err
	}
	return provider, values[3], nil
}

func validateMediaCDNBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("CLOUDFLARE_MEDIA_BASE_URL must be an HTTPS origin without path, query or fragment")
	}
	return nil
}
