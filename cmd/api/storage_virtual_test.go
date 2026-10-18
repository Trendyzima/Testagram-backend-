package main

import (
	"testing"
)

func TestPublicMediaURLKeepsLegacyAndMapsVirtualKeys(t *testing.T) {
	t.Setenv("MEDIA_PUBLIC_BASE_URL", "https://media.example.test/v1")
	legacy := publicMediaURLForStorage("users/user-1/0123456789abcdef0123456789abcdef", "user-1", "legacy-media")
	if legacy != "https://media.example.test/v1/users/user-1/0123456789abcdef0123456789abcdef" {
		t.Fatalf("legacy URL changed: %s", legacy)
	}
	virtual := publicMediaURLForStorage("users/user-1/0123456789abcdef0123456789abcdef", "user-1", "media-public")
	if virtual != "https://media.example.test/v1/tenants/user-1/users/user-1/0123456789abcdef0123456789abcdef" {
		t.Fatalf("virtual URL did not include tenant-scoped physical key: %s", virtual)
	}

	t.Setenv("MEDIA_PUBLIC_BASE_URL", "https://media.example.test/testagram-public-media")
	bucketURL := publicMediaURLForStorage("users/user-1/0123456789abcdef0123456789abcdef", "user-1", "media-public")
	if bucketURL != "https://media.example.test/testagram-public-media/tenants/user-1/users/user-1/0123456789abcdef0123456789abcdef" {
		t.Fatalf("virtual URL does not map to the public bucket origin path: %s", bucketURL)
	}
}
