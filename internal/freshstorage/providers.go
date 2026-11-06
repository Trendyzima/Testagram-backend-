package freshstorage

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

const TargetPoolBytes int64 = 100_000_000_000

type ProviderSet struct {
	B2       virtualstorage.Provider
	B2Bucket string
	R2       virtualstorage.Provider
	R2Bucket string
}

func FromEnv() (ProviderSet, error) {
	var out ProviderSet
	var err error
	out.B2, out.B2Bucket, err = providerFromEnv("B2_S3_ENDPOINT", "B2_S3_REGION", "B2_S3_ACCESS_KEY_ID", "B2_S3_SECRET_ACCESS_KEY", "B2_S3_BUCKET", "us-west-004")
	if err != nil {
		return ProviderSet{}, fmt.Errorf("Backblaze B2: %w", err)
	}
	out.R2, out.R2Bucket, err = providerFromEnv("CLOUDFLARE_R2_ENDPOINT", "CLOUDFLARE_R2_REGION", "CLOUDFLARE_R2_ACCESS_KEY_ID", "CLOUDFLARE_R2_SECRET_ACCESS_KEY", "CLOUDFLARE_R2_BUCKET", "auto")
	if err != nil {
		return ProviderSet{}, fmt.Errorf("Cloudflare R2: %w", err)
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
