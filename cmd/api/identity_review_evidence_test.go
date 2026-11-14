package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchIdentityEvidenceBytesVerifiesContentAndDigest(t *testing.T) {
	payload := []byte("%PDF-1.7\nverified evidence\n%%EOF\n")
	digest := sha256.Sum256(payload)
	expected := hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	got, err := fetchIdentityEvidenceBytes(context.Background(), server.URL, int64(len(payload)), "application/pdf", expected)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("evidence bytes=%q, want %q", got, payload)
	}
}

func TestFetchIdentityEvidenceBytesRejectsChecksumMismatch(t *testing.T) {
	payload := []byte("%PDF-1.7\nverified evidence\n%%EOF\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	_, err := fetchIdentityEvidenceBytes(context.Background(), server.URL, int64(len(payload)), "application/pdf", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("checksum-mismatched evidence was accepted")
	}
}

func TestFetchIdentityEvidenceBytesRejectsContentTypeMismatch(t *testing.T) {
	payload := []byte("%PDF-1.7\nverified evidence\n%%EOF\n")
	digest := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	_, err := fetchIdentityEvidenceBytes(context.Background(), server.URL, int64(len(payload)), "application/pdf", hex.EncodeToString(digest[:]))
	if err == nil {
		t.Fatal("content-type-mismatched evidence was accepted")
	}
}

func TestFetchIdentityEvidenceBytesRejectsOversizedResponse(t *testing.T) {
	payload := []byte("%PDF-1.7\nverified evidence\n%%EOF\n")
	digest := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(append(payload, 'X'))
	}))
	defer server.Close()

	_, err := fetchIdentityEvidenceBytes(context.Background(), server.URL, int64(len(payload)), "application/pdf", hex.EncodeToString(digest[:]))
	if err == nil {
		t.Fatal("oversized evidence response was accepted")
	}
}

func TestFetchIdentityEvidenceBytesDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7\n"))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	payload := []byte("%PDF-1.7\n")
	digest := sha256.Sum256(payload)
	_, err := fetchIdentityEvidenceBytes(context.Background(), redirect.URL, int64(len(payload)), "application/pdf", hex.EncodeToString(digest[:]))
	if err == nil {
		t.Fatal("redirected evidence response was accepted")
	}
}
