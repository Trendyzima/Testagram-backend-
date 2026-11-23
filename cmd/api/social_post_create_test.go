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

func TestExtractSocialMentionHandles(t *testing.T) {
	got := extractSocialMentionHandles("Hello @Alice, then @bob and again @ALICE. Ignore email@host.test and @x.")
	want := []string{"alice", "bob"}
	if len(got) != len(want) {
		t.Fatalf("mentions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mentions = %#v, want %#v", got, want)
		}
	}
}

func TestExtractSocialMentionHandlesIgnoresMalformedAndEmbeddedHandles(t *testing.T) {
	for _, content := range []string{
		"email@host.test",
		"prefix@alice",
		"@x",
		"@@alice",
		"@this_handle_is_far_too_long_for_a_valid_account_handle",
	} {
		if got := extractSocialMentionHandles(content); len(got) != 0 {
			t.Errorf("extractSocialMentionHandles(%q) = %#v, want none", content, got)
		}
	}
}
