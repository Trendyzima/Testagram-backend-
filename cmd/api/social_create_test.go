package main

import "testing"

func TestSocialIdempotencyKeyHash(t *testing.T) {
	first, err := socialIdempotencyKeyHash("post-create-123456")
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	second, err := socialIdempotencyKeyHash("post-create-123456")
	if err != nil || first != second {
		t.Fatalf("same key must hash deterministically: %q %q err=%v", first, second, err)
	}
	if _, err := socialIdempotencyKeyHash("short"); err == nil {
		t.Fatal("short idempotency key must be rejected")
	}
	if _, err := socialIdempotencyKeyHash("post-create-123456\r\nInjected: yes"); err == nil {
		t.Fatal("header injection must be rejected")
	}
}

func TestSocialPostRequestHashIncludesAllMutableFields(t *testing.T) {
	base := socialCreatePostRequest{Content: "hello", Visibility: "public"}
	if socialPostRequestHash(base) == socialPostRequestHash(socialCreatePostRequest{Content: "hello", Visibility: "followers"}) {
		t.Fatal("visibility must be part of the idempotency request hash")
	}
	if socialPostRequestHash(base) == socialPostRequestHash(socialCreatePostRequest{Content: "hello", Visibility: "public", Sensitive: true}) {
		t.Fatal("sensitive flag must be part of the idempotency request hash")
	}
	if socialPostRequestHash(base) != socialPostRequestHash(base) {
		t.Fatal("same normalized post request must hash deterministically")
	}
}
