package main

import (
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/virtualstorage"
)

func TestValidateMediaReplicaResultsRequiresThreeIndependentVerifiedProviders(t *testing.T) {
	job := mediaReplicationJob{
		Operation: "replicate",
		SizeBytes: 17,
		Checksum:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	valid := []virtualstorage.ReplicaResult{
		{Provider: "instabox-vps", Bucket: "private-primary", Size: 17, SHA256: job.Checksum},
		{Provider: "cloudflare-r2", Bucket: "private-r2", Size: 17, SHA256: job.Checksum},
		{Provider: "backblaze-b2", Bucket: "private-b2", Size: 17, SHA256: job.Checksum},
	}
	if err := validateMediaReplicaResults(job, valid); err != nil {
		t.Fatalf("valid three-provider result rejected: %v", err)
	}

	tests := []struct {
		name    string
		results []virtualstorage.ReplicaResult
	}{
		{name: "missing provider", results: valid[:2]},
		{name: "duplicate provider", results: []virtualstorage.ReplicaResult{valid[0], valid[0], valid[2]}},
		{name: "unknown provider", results: []virtualstorage.ReplicaResult{valid[0], valid[1], {Provider: "other", Bucket: "other", Size: 17, SHA256: job.Checksum}}},
		{name: "wrong size", results: []virtualstorage.ReplicaResult{valid[0], valid[1], {Provider: "backblaze-b2", Bucket: "private-b2", Size: 18, SHA256: job.Checksum}}},
		{name: "wrong digest", results: []virtualstorage.ReplicaResult{valid[0], valid[1], {Provider: "backblaze-b2", Bucket: "private-b2", Size: 17, SHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}}},
		{name: "missing bucket", results: []virtualstorage.ReplicaResult{valid[0], valid[1], {Provider: "backblaze-b2", Size: 17, SHA256: job.Checksum}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMediaReplicaResults(job, test.results); err == nil {
				t.Fatal("invalid replica set was accepted")
			}
		})
	}
}
