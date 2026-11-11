package main

import "testing"

func TestValidSocialPostIdempotencyKey(t *testing.T) {
	for _, value := range []string{
		"post-retry-key-0001",
		"12345678",
		"0123456789abcdef0123456789abcdef",
	} {
		if !validSocialPostIdempotencyKey(value) {
			t.Fatalf("expected valid key %q", value)
		}
	}
	for _, value := range []string{
		"", "short", "bad key 123", "newline\nkey123", "slash/key123",
		string(make([]byte, 129)),
	} {
		if validSocialPostIdempotencyKey(value) {
			t.Fatalf("expected invalid key %q", value)
		}
	}
}

func TestSocialPostRequestHashNormalizesValidatedInput(t *testing.T) {
	first, err := socialPostRequestHash(socialPostCreateInput{
		Content: "Hello Testagram", Visibility: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := socialPostRequestHash(socialPostCreateInput{
		Content: "Hello Testagram", Visibility: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := socialPostRequestHash(socialPostCreateInput{
		Content: "Hello Testagram", Visibility: "followers",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("identical normalized requests must have the same hash")
	}
	if first == changed {
		t.Fatal("different visibility must produce a different request hash")
	}
}
