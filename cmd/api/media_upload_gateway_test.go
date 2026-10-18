package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCopyBoundedUploadAcceptsExactLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	var dst bytes.Buffer
	n, err := copyBoundedUpload(recorder, io.NopCloser(bytes.NewReader([]byte("12345678"))), 8, &dst)
	if err != nil {
		t.Fatalf("exact-limit upload rejected: %v", err)
	}
	if n != 8 || dst.String() != "12345678" {
		t.Fatalf("unexpected copied bytes: n=%d body=%q", n, dst.String())
	}
}

func TestCopyBoundedUploadRejectsOverflowDuringRead(t *testing.T) {
	recorder := httptest.NewRecorder()
	var dst bytes.Buffer
	n, err := copyBoundedUpload(recorder, io.NopCloser(bytes.NewReader([]byte("123456789"))), 8, &dst)
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("expected http.MaxBytesError for overflow, got n=%d err=%v", n, err)
	}
	if n != 8 || dst.String() != "12345678" {
		t.Fatalf("overflow bytes escaped limit: n=%d body=%q", n, dst.String())
	}
}

func TestCopyBoundedUploadRejectsInvalidLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	var dst bytes.Buffer
	if _, err := copyBoundedUpload(recorder, io.NopCloser(bytes.NewReader([]byte("x"))), 0, &dst); err == nil {
		t.Fatal("expected non-positive limit to be rejected")
	}
	if dst.Len() != 0 {
		t.Fatalf("copied bytes despite invalid limit: %d", dst.Len())
	}
}
