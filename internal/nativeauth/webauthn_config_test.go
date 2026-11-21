package nativeauth

import "testing"

func TestWebAuthnConfigurationFingerprintIsOrderIndependentAndExact(t *testing.T) {
	one := WebAuthnConfigurationFingerprint("OPS.TESTAGRAM.SITE", []string{
		"https://ops.testagram.site",
		"https://admin.testagram.site",
	})
	two := WebAuthnConfigurationFingerprint("ops.testagram.site", []string{
		"https://admin.testagram.site",
		"https://ops.testagram.site",
	})
	if one == "" || one != two {
		t.Fatalf("equivalent RP configuration fingerprints differ: %q != %q", one, two)
	}
	if one == WebAuthnConfigurationFingerprint("ops.testagram.site", []string{"https://ops.testagram.site"}) {
		t.Fatal("fingerprint ignored an allowed-origin difference")
	}
	if one == WebAuthnConfigurationFingerprint("other.testagram.site", []string{
		"https://ops.testagram.site",
		"https://admin.testagram.site",
	}) {
		t.Fatal("fingerprint ignored an RP ID difference")
	}
	if WebAuthnConfigurationFingerprint("", []string{"https://ops.testagram.site"}) != "" {
		t.Fatal("empty RP ID produced a readiness fingerprint")
	}
	if WebAuthnConfigurationFingerprint("ops.testagram.site", nil) != "" {
		t.Fatal("empty origin set produced a readiness fingerprint")
	}
}
