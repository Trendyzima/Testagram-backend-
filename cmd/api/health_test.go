package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", got)
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

func TestHealthIsCanonicalPublicProbeAndMatchesHealthz(t *testing.T) {
	for _, path := range []string{"/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			(&service{}).routes().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d; body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := rec.Header().Get("X-Testagram-Service"); got != "testagram-api" {
				t.Fatalf("X-Testagram-Service = %q, want testagram-api", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
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
		})
	}
}

func TestEdgeRecoveryReadinessRequiresDedicatedBearerSecret(t *testing.T) {
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("x", 32))
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	svc := &service{edgeRecoveryReadinessToken: token}
	for _, tc := range []struct {
		name          string
		authorization string
		want          int
	}{
		{name: "missing", want: http.StatusNotFound},
		{name: "wrong", authorization: "Bearer wrong-token", want: http.StatusNotFound},
		{name: "valid but database unavailable", authorization: "Bearer " + token, want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/edge/recovery-ready", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()
			svc.routes().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("recovery readiness response must be no-store")
			}
		})
	}
}

func TestEdgeRecoveryReadinessRequiresNativeAuthConfiguration(t *testing.T) {
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", "")
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	req := httptest.NewRequest(http.MethodGet, "/internal/edge/recovery-ready", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	(&service{edgeRecoveryReadinessToken: token}).routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness without native auth signing configuration status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "JWT") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("readiness response leaked configuration details: %s", rec.Body.String())
	}
}

func TestEdgeRecoveryReadinessRemainsHiddenWithoutConfiguredSecret(t *testing.T) {
	rec := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/edge/recovery-ready", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured recovery readiness status = %d, want 404", rec.Code)
	}
}
