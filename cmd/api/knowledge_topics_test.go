package main

import (
	"reflect"
	"testing"
)

func TestNormalizeKnowledgeTopicSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "  PostgreSQL  ", want: "postgresql", ok: true},
		{input: "kenya-tech", want: "kenya-tech", ok: true},
		{input: "ai-2026", want: "ai-2026", ok: true},
		{input: "-invalid", ok: false},
		{input: "has space", ok: false},
		{input: "économie", ok: false},
		{input: "x", ok: false},
		{input: string(make([]byte, 64)), ok: false},
	}
	for _, tt := range tests {
		got, ok := normalizeKnowledgeTopicSlug(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("normalizeKnowledgeTopicSlug(%q)=(%q,%v), want (%q,%v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNormalizeKnowledgeTopicSlugsDeduplicatesAndSorts(t *testing.T) {
	got, ok := normalizeKnowledgeTopicSlugs([]string{"PostgreSQL", "kenya-tech", "postgresql", "AI"})
	if !ok {
		t.Fatal("valid topic slugs were rejected")
	}
	want := []string{"ai", "kenya-tech", "postgresql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized topics=%v, want %v", got, want)
	}
}

func TestNormalizeKnowledgeTopicSlugsRejectsInvalidAndExcessiveInput(t *testing.T) {
	if _, ok := normalizeKnowledgeTopicSlugs([]string{"valid", "bad topic"}); ok {
		t.Fatal("invalid topic slug was accepted")
	}
	if _, ok := normalizeKnowledgeTopicSlugs([]string{"one", "two", "three", "four", "five", "six"}); ok {
		t.Fatal("more than five topic slugs were accepted")
	}
}

func TestQuestionTopicExtensionPreservesLegacyIdempotencyHashWithoutTopics(t *testing.T) {
	legacy := struct {
		Title      string `json:"title"`
		Body       string `json:"body"`
		Visibility string `json:"visibility"`
		Language   string `json:"language"`
	}{
		Title: "How does PostgreSQL work?",
		Body: "Explain MVCC.",
		Visibility: "public",
		Language: "en",
	}
	current := knowledgeQuestionInput{
		Title: legacy.Title,
		Body: legacy.Body,
		Visibility: legacy.Visibility,
		Language: legacy.Language,
	}
	oldHash, err := knowledgeHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := knowledgeHash(current)
	if err != nil {
		t.Fatal(err)
	}
	if newHash != oldHash {
		t.Fatal("adding optional topic tags must not invalidate retries for legacy questions")
	}
}
