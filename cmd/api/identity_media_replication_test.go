package main

import "testing"

func TestValidateIdentityEvidenceBucketIsolation(t *testing.T) {
	tests := []struct {
		name    string
		buckets map[string]string
		wantErr bool
	}{
		{
			name: "identity evidence may share private physical bucket",
			buckets: map[string]string{
				"media-private": "private-media",
				"media-public": "public-media",
				"identity-evidence-private": "private-media",
			},
		},
		{
			name: "dedicated identity evidence bucket",
			buckets: map[string]string{
				"media-private": "private-media",
				"media-public": "public-media",
				"identity-evidence-private": "private-identity",
			},
		},
		{
			name: "identity evidence cannot use public bucket",
			buckets: map[string]string{
				"media-private": "private-media",
				"media-public": "public-media",
				"identity-evidence-private": "public-media",
			},
			wantErr: true,
		},
		{
			name: "identity evidence bucket required",
			buckets: map[string]string{
				"media-private": "private-media",
				"media-public": "public-media",
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateIdentityEvidenceBucketIsolation("test-provider", test.buckets)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateIdentityEvidenceBucketIsolation() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
