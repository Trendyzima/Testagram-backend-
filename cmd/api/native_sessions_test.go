package main

import "testing"

func TestNativeAuthenticationCannotBeDisabledByLegacyProviderSetting(t *testing.T) {
	for _, legacyValue := range []string{"", "appwrite", "APPWRITE", "supabase", "external"} {
		t.Setenv("TESTAGRAM_AUTH_PROVIDER", legacyValue)
		if !nativeAuthEnabled() {
			t.Fatalf("native authentication was disabled by legacy provider setting %q", legacyValue)
		}
	}
}
