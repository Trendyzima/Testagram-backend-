package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

func newMediaReplicaProvider(prefix, defaultRegion string) (*virtualstorage.S3Provider, error) {
	endpoint := strings.TrimSpace(os.Getenv(prefix + "_ENDPOINT"))
	accessKey := strings.TrimSpace(os.Getenv(prefix + "_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv(prefix + "_SECRET_ACCESS_KEY"))
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("%s endpoint, access key and secret key are required", prefix)
	}
	region := strings.TrimSpace(os.Getenv(prefix + "_REGION"))
	if region == "" {
		region = defaultRegion
	}
	provider, err := virtualstorage.NewS3Provider(virtualstorage.S3Config{
		Endpoint: endpoint,
		Region: region,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Client: &http.Client{Timeout: 8 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("%s provider: %w", prefix, err)
	}
	return provider, nil
}

func mediaReplicaBucket(prefix, visibility, fallback string) string {
	name := prefix + "_" + strings.ToUpper(visibility) + "_BUCKET"
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(fallback))
}

func mediaReplicaTargetsFromEnv() ([]virtualstorage.ReplicaTarget, error) {
	r2, err := newMediaReplicaProvider("CLOUDFLARE_R2", "auto")
	if err != nil { return nil, err }
	b2, err := newMediaReplicaProvider("B2_S3", "us-east-005")
	if err != nil { return nil, err }
	r2Private := mediaReplicaBucket("CLOUDFLARE_R2", "private", "CLOUDFLARE_R2_BUCKET")
	r2Public := mediaReplicaBucket("CLOUDFLARE_R2", "public", "CLOUDFLARE_R2_BUCKET")
	b2Private := mediaReplicaBucket("B2_S3", "private", "B2_S3_BUCKET")
	b2Public := mediaReplicaBucket("B2_S3", "public", "B2_S3_BUCKET")
	if r2Private == "" || r2Public == "" || b2Private == "" || b2Public == "" {
		return nil, errors.New("both private and public buckets must be configured for Cloudflare R2 and Backblaze B2")
	}
	return []virtualstorage.ReplicaTarget{
		{Name: "cloudflare-r2", Provider: r2, Buckets: map[string]string{"media-private": r2Private, "media-public": r2Public}},
		{Name: "backblaze-b2", Provider: b2, Buckets: map[string]string{"media-private": b2Private, "media-public": b2Public}},
	}, nil
}
