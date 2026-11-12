package main

import "testing"

func TestValidateMediaBucketIsolation(t *testing.T) {
	tests := []struct {
		name    string
		buckets map[string]string
		wantErr bool
	}{
		{name: "distinct buckets", buckets: map[string]string{"media-private": "testagram-private-media", "media-public": "testagram-public-media"}},
		{name: "same physical bucket", buckets: map[string]string{"media-private": "testagram-media", "media-public": "testagram-media"}, wantErr: true},
		{name: "missing private bucket", buckets: map[string]string{"media-public": "testagram-public-media"}, wantErr: true},
		{name: "missing public bucket", buckets: map[string]string{"media-private": "testagram-private-media"}, wantErr: true},
		{name: "whitespace normalized", buckets: map[string]string{"media-private": " testagram-private-media ", "media-public": "testagram-public-media"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMediaBucketIsolation("test-provider", test.buckets)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateMediaBucketIsolation() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
