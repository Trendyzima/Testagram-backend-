package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalProductCapabilityIsDisabledByDefault(t *testing.T) {
	t.Setenv("TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED", "false")
	s := &service{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/capability", nil)
	if handled := s.localProductCapability(w, r, "request-1", capabilityRequest{Capability: "testagram.posts.create"}); handled {
		t.Fatal("local write must not intercept requests when the feature flag is off")
	}
}

func TestLocalProductCapabilityFailsClosedWithoutNativeSessionStore(t *testing.T) {
	t.Setenv("TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED", "true")
	t.Setenv("TESTAGRAM_AUTH_PROVIDER", "native")
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", "test-secret-long-enough-for-native-auth")
	s := &service{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/capability", nil)
	r.Header.Set("Authorization", "Bearer a.valid-looking-token")
	if handled := s.localProductCapability(w, r, "request-2", capabilityRequest{Capability: "testagram.posts.create"}); !handled {
		t.Fatal("expected local write capability to be handled")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
