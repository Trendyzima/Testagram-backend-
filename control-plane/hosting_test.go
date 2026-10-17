package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHostingRoutesRequireInternalBearer(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	tests := []struct {
		name string
		path string
		handler http.HandlerFunc
	}{
		{"domains", "/v1/hosting/domains", s.hostingDomains},
		{"deployments", "/v1/hosting/deployments", s.hostingDeployments},
		{"usage", "/v1/hosting/usage", s.hostingUsage},
		{"backups", "/v1/hosting/backups", s.hostingBackups},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			tt.handler(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("wanted 401, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNormalizedBaseDomain(t *testing.T) {
	t.Setenv("TESTAGRAM_BASE_DOMAIN", "Example.Testagram.Site.")
	got, err := normalizedBaseDomain()
	if err != nil || got != "example.testagram.site" {
		t.Fatalf("unexpected normalized domain %q, err=%v", got, err)
	}
	for _, bad := range []string{"", "127.0.0.1", "https://testagram.site", "-bad.testagram.site", "bad..site"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("TESTAGRAM_BASE_DOMAIN", bad)
			if _, err := normalizedBaseDomain(); err == nil {
				t.Fatalf("expected invalid domain %q to fail", bad)
			}
		})
	}
}

func TestReservedDomainSlugs(t *testing.T) {
	for _, slug := range []string{"www", "api", "admin", "auth", "mail", "xn--example"} {
		if !isReservedDomainSlug(slug) {
			t.Errorf("expected %q to be reserved", slug)
		}
	}
	for _, slug := range []string{"alice", "testagram-app", "project7"} {
		if isReservedDomainSlug(slug) {
			t.Errorf("did not expect %q to be reserved", slug)
		}
	}
}

func TestDomainTargetsRejectCredentialsAndQueries(t *testing.T) {
	t.Setenv("TESTAGRAM_DOMAIN_TARGETS", "web=http://web:3000,unsafe=http://user:pass@127.0.0.1:2375?token=x,query=http://web:3000/path?q=x")
	targets := domainTargets()
	if targets["web"] != "http://web:3000" {
		t.Fatalf("expected valid service target, got %#v", targets)
	}
	if _, ok := targets["unsafe"]; ok {
		t.Fatal("target with credentials/query must be rejected")
	}
	if _, ok := targets["query"]; ok {
		t.Fatal("target with query must be rejected")
	}
}

func TestDeploymentRejectsSecretLikePublicEnvBeforeDatabase(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	body := `{"appName":"demo","image":"registry.example/demo","imageDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publicEnv":{"API_TOKEN":"should-not-be-here"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/hosting/deployments", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer unit-test-token")
	rec := httptest.NewRecorder()
	s.hostingDeployments(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wanted 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeploymentDoesNotDispatchUnresolvedSecrets(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	body := `{"appName":"demo","image":"registry.example/demo","imageDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","secretRefs":{"DB_PASSWORD":"secret://production/db-password"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/hosting/deployments", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer unit-test-token")
	rec := httptest.NewRecorder()
	s.hostingDeployments(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("wanted 501, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "production/db-password") {
		t.Fatal("response must not echo secret references")
	}
}

func TestDomainReservationNeedsConfiguredBaseDomainAndAllowlistedTarget(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	oldDomain, hadDomain := os.LookupEnv("TESTAGRAM_BASE_DOMAIN")
	oldTargets, hadTargets := os.LookupEnv("TESTAGRAM_DOMAIN_TARGETS")
	t.Cleanup(func() {
		if hadDomain { _ = os.Setenv("TESTAGRAM_BASE_DOMAIN", oldDomain) } else { _ = os.Unsetenv("TESTAGRAM_BASE_DOMAIN") }
		if hadTargets { _ = os.Setenv("TESTAGRAM_DOMAIN_TARGETS", oldTargets) } else { _ = os.Unsetenv("TESTAGRAM_DOMAIN_TARGETS") }
	})
	_ = os.Unsetenv("TESTAGRAM_BASE_DOMAIN")
	_ = os.Setenv("TESTAGRAM_DOMAIN_TARGETS", "web=http://web:3000")
	body := `{"slug":"my-app","targetService":"web"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/hosting/domains", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer unit-test-token")
	rec := httptest.NewRecorder()
	s.hostingDomains(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("wanted 503 when no base domain is configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDomainActivationFailsClosedWithoutExpectedOriginIP(t *testing.T) {
	t.Setenv("TESTAGRAM_BASE_DOMAIN", "testagram.site")
	t.Setenv("TESTAGRAM_PUBLIC_ORIGIN_IP", "")
	s := &Server{internal: "unit-test-token"}
	req := httptest.NewRequest(http.MethodPost, "/v1/hosting/domains/demo/activate", nil)
	req.Header.Set("Authorization", "Bearer unit-test-token")
	rec := httptest.NewRecorder()
	s.hostingDomainActions(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("wanted 503 without expected origin IP, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTLSAuthorizationRejectsHostOutsideManagedZone(t *testing.T) {
	t.Setenv("TESTAGRAM_BASE_DOMAIN", "testagram.site")
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/v1/hosting/tls/authorize?domain=attacker.example", nil)
	rec := httptest.NewRecorder()
	s.hostingTLSAuthorize(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wanted 403 for external host, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestManagedDomainRoutingFailsClosedForApexReservedAndNestedHosts(t *testing.T) {
	t.Setenv("TESTAGRAM_BASE_DOMAIN", "testagram.site")
	s := &Server{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	tests := []struct {
		host string
		want int
	}{
		{host: "testagram.site", want: http.StatusNotFound},
		{host: "api.testagram.site", want: http.StatusNotFound},
		{host: "foo.bar.testagram.site", want: http.StatusNotFound},
		{host: "unrelated.example", want: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://"+tt.host+"/", nil)
			rec := httptest.NewRecorder()
			s.withManagedDomainRouting(next).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("host %q: wanted %d, got %d", tt.host, tt.want, rec.Code)
			}
		})
	}
}

func TestTLSAuthorizationRejectsWhenOriginIPIsNotConfigured(t *testing.T) {
	t.Setenv("TESTAGRAM_BASE_DOMAIN", "testagram.site")
	t.Setenv("TESTAGRAM_PUBLIC_ORIGIN_IP", "")
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/v1/hosting/tls/authorize?domain=demo.testagram.site", nil)
	rec := httptest.NewRecorder()
	s.hostingTLSAuthorize(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wanted 403 without configured origin IP, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNodeMaintenanceRequiresInternalBearer(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	req := httptest.NewRequest(http.MethodPost, "/v1/hosting/nodes/00000000-0000-0000-0000-000000000000/drain", nil)
	rec := httptest.NewRecorder()
	s.hostingNodeActions(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wanted 401 without bearer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNodeMaintenanceRejectsUnknownActionBeforeDatabase(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	req := httptest.NewRequest(http.MethodPost, "/v1/hosting/nodes/00000000-0000-0000-0000-000000000000/reboot", nil)
	req.Header.Set("Authorization", "Bearer unit-test-token")
	rec := httptest.NewRecorder()
	s.hostingNodeActions(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wanted 404 for unsupported action, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHostingAuditRejectsInvalidLimitBeforeDatabase(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	req := httptest.NewRequest(http.MethodGet, "/v1/hosting/audit?limit=501", nil)
	req.Header.Set("Authorization", "Bearer unit-test-token")
	rec := httptest.NewRecorder()
	s.hostingAuditEvents(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wanted 400 for out-of-range audit limit, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHostingAuditRequiresInternalBearer(t *testing.T) {
	s := &Server{internal: "unit-test-token"}
	req := httptest.NewRequest(http.MethodGet, "/v1/hosting/audit", nil)
	rec := httptest.NewRecorder()
	s.hostingAuditEvents(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wanted 401 without bearer, got %d: %s", rec.Code, rec.Body.String())
	}
}
