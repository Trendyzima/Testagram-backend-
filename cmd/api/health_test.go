package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzIdentifiesCanonicalBackend(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	(&service{}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("X-Testagram-Service"); got != "testagram-api" {
		t.Fatalf("X-Testagram-Service = %q, want testagram-api", got)
	}
	if got := rec.Header().Get("X-Testagram-Health-Contract"); got != "v1" {
		t.Fatalf("X-Testagram-Health-Contract = %q, want v1", got)
	}
	var body struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health body: %v", err)
	}
	if !body.OK || body.Service != "testagram-api" {
		t.Fatalf("health body = %+v, want ok=true service=testagram-api", body)
	}
}


func TestReadyzFailsClosedWithoutDatabase(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	(&service{}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz without PostgreSQL status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Fatal("readiness must not pass when PostgreSQL is unavailable")
	}
}
