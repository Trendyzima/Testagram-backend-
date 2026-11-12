package main

import (
	"regexp"
	"testing"
)

func TestAvatarImageTypeAcceptsOnlySupportedMagicBytes(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0x00}, "image/jpeg"},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, "image/png"},
		{"webp", []byte("RIFF0000WEBP"), "image/webp"},
		{"spoofed extension is irrelevant", []byte("not an image"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := avatarImageType(tc.data)
			if tc.want == "" && err == nil {
				t.Fatal("expected unsupported image bytes to be rejected")
			}
			if tc.want != "" && (err != nil || got != tc.want) {
				t.Fatalf("type=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestNewAvatarObjectKeyUsesMediaLedgerKeyFormat(t *testing.T) {
	key, err := newAvatarObjectKey("123e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^users/[A-Za-z0-9-]{1,128}/[a-f0-9]{32}$`).MatchString(key) {
		t.Fatalf("avatar key %q does not satisfy media ledger format", key)
	}
}
