package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

func validateIdentityEvidenceBucketIsolation(provider string, buckets map[string]string) error {
	if err := validateMediaBucketIsolation(provider, buckets); err != nil {
		return err
	}
	identityBucket := strings.TrimSpace(buckets["identity-evidence-private"])
	privateBucket := strings.TrimSpace(buckets["media-private"])
	publicBucket := strings.TrimSpace(buckets["media-public"])
	if identityBucket == "" || identityBucket == privateBucket || identityBucket == publicBucket {
		return fmt.Errorf("%s identity evidence bucket must be dedicated and distinct from private/public media buckets", provider)
	}
	return nil
}

// configureIdentityMediaReplicator extends the three-provider media topology
// with the private identity-evidence namespace. Identity evidence is replicated
// and checksum-read-back verified before an applicant can enter manual review.
func configureIdentityMediaReplicator(primary *virtualstorage.S3Provider) (*virtualstorage.Replicator, error) {
	if primary == nil {
		return nil, errors.New("Instabox VPS primary provider is not configured")
	}
	r2, err := newMediaReplicaProvider("CLOUDFLARE_R2", "auto")
	if err != nil {
		return nil, err
	}
	b2, err := newMediaReplicaProvider("B2_S3", "")
	if err != nil {
		return nil, err
	}

	primaryBuckets := map[string]string{
		"media-private": env("MEDIA_PRIVATE_BUCKET", env("R2_PRIVATE_BUCKET", env("MINIO_BUCKET", "testagram-private-media"))),
		"media-public":  env("MEDIA_PUBLIC_BUCKET", env("R2_PUBLIC_BUCKET", env("PUBLIC_MEDIA_BUCKET", "testagram-public-media"))),
	}
	primaryBuckets["identity-evidence-private"] = strings.TrimSpace(os.Getenv("IDENTITY_EVIDENCE_PRIVATE_BUCKET"))

	r2Buckets := map[string]string{
		"media-private": replicaBucket("CLOUDFLARE_R2", "private", "CLOUDFLARE_R2_BUCKET"),
		"media-public":  replicaBucket("CLOUDFLARE_R2", "public", "CLOUDFLARE_R2_BUCKET"),
	}
	r2Buckets["identity-evidence-private"] = strings.TrimSpace(os.Getenv("CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET"))

	b2Buckets := map[string]string{
		"media-private": replicaBucket("B2_S3", "private", "B2_S3_BUCKET"),
		"media-public":  replicaBucket("B2_S3", "public", "B2_S3_BUCKET"),
	}
	b2Buckets["identity-evidence-private"] = strings.TrimSpace(os.Getenv("B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET"))

	for provider, buckets := range map[string]map[string]string{
		"instabox-vps":  primaryBuckets,
		"cloudflare-r2": r2Buckets,
		"backblaze-b2":  b2Buckets,
	} {
		if err := validateIdentityEvidenceBucketIsolation(provider, buckets); err != nil {
			return nil, err
		}
	}

	return virtualstorage.NewReplicator("instabox-vps", primary, primaryBuckets, []virtualstorage.ReplicaTarget{
		{Name: "cloudflare-r2", Provider: r2, Buckets: r2Buckets},
		{Name: "backblaze-b2", Provider: b2, Buckets: b2Buckets},
	})
}
