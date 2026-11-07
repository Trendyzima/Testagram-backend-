package main

import (
	"strings"
	"testing"
)

func TestFederationTokenMatches(t *testing.T) {
	const secret = "federation-internal-token-with-more-than-32-characters"
	tests := []struct {
		name          string
		expected      string
		authorization string
		want          bool
	}{
		{"valid bearer", secret, "Bearer " + secret, true},
		{"case insensitive scheme", secret, "bearer " + secret, true},
		{"wrong secret", secret, "Bearer wrong-secret", false},
		{"missing bearer", secret, secret, false},
		{"missing configured secret", "", "Bearer " + secret, false},
		{"short configured secret", "short", "Bearer short", false},
		{"extra authorization fields", secret, "Bearer " + secret + " extra", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := federationTokenMatches(test.expected, test.authorization); got != test.want {
				t.Fatalf("federationTokenMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFederationHandlePattern(t *testing.T) {
	for _, handle := range []string{"alice", "user_123", "UPPERCASE"} {
		if !localAccountUsernamePattern.MatchString(handle) {
			t.Errorf("valid handle %q was rejected", handle)
		}
	}
	for _, handle := range []string{"ab", "contains-dash", "bad handle", strings.Repeat("x", 31)} {
		if localAccountUsernamePattern.MatchString(handle) {
			t.Errorf("invalid handle %q was accepted", handle)
		}
	}
}
