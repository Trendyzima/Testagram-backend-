package main

import "testing"

func TestKnowledgeHashIsStableAndContentSensitive(t *testing.T) {
	a, err := knowledgeHash(knowledgeQuestionInput{Title: "Why PostgreSQL?", Body: "Explain MVCC.", Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := knowledgeHash(knowledgeQuestionInput{Title: "Why PostgreSQL?", Body: "Explain MVCC.", Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := knowledgeHash(knowledgeQuestionInput{Title: "Why PostgreSQL?", Body: "Explain MVCC.", Visibility: "followers"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("identical normalized question payloads must hash identically")
	}
	if a == c {
		t.Fatal("visibility changes must change the idempotency request hash")
	}
}

func TestKnowledgeIdempotencyKeyHashDoesNotExposeRawKey(t *testing.T) {
	key := "question-retry-key-0001"
	got := knowledgeKeyHash(key)
	if got == key || len(got) != 64 {
		t.Fatalf("expected SHA-256 digest, got %q", got)
	}
	if got != knowledgeKeyHash(key) {
		t.Fatal("key hash must be deterministic")
	}
}

func TestKnowledgeVisibilityPolicyIncludesBlocksAndProtectedAccounts(t *testing.T) {
	for _, fragment := range []string{"q.visibility='public' AND NOT a.protected_account", "f.status='accepted'", "testagram_local.blocks", "testagram_local.mutes", "q.deleted_at IS NULL"} {
		if !contains(knowledgeVisible, fragment) {
			t.Fatalf("visibility policy is missing %q", fragment)
		}
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
