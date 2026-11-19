package main

import (
	"testing"
	"time"
)

func TestIdentityReviewRequiresFreshPhishingResistantMFAClaim(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name string
		mfaAt int64
		wantErr bool
	}{
		{name: "missing proof", mfaAt: 0, wantErr: true},
		{name: "expired proof", mfaAt: now - 301, wantErr: true},
		{name: "future proof outside clock skew", mfaAt: now + 61, wantErr: true},
		{name: "fresh verified proof", mfaAt: now - 30, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireLiveIdentityReviewMFA(authClaims{MFAAuthenticatedAt: tc.mfaAt})
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireLiveIdentityReviewMFA() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
