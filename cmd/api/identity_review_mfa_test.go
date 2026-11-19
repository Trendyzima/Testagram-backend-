package main

import "testing"

func TestIdentityReviewRequiresLivePhishingResistantMFAClaim(t *testing.T) {
	if err := requireLiveIdentityReviewMFA(authClaims{}); err == nil {
		t.Fatal("staff enrollment marker without a live MFA claim must not authorize identity review")
	}
	if err := requireLiveIdentityReviewMFA(authClaims{MFAAuthenticated: true}); err != nil {
		t.Fatalf("fresh verified MFA claim was rejected: %v", err)
	}
}
