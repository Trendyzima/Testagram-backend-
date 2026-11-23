package main

import "testing"

func TestNormalizeSocialListInput(t *testing.T) {
	tests := []struct {
		name           string
		input          socialListInput
		wantOK         bool
		wantVisibility string
	}{
		{name: "defaults private and trims", input: socialListInput{Name: "  Kenya builders  ", Description: "  Local tech community  "}, wantOK: true, wantVisibility: "private"},
		{name: "public list", input: socialListInput{Name: "Research", Visibility: "PUBLIC"}, wantOK: true, wantVisibility: "public"},
		{name: "unlisted list", input: socialListInput{Name: "Friends", Visibility: " unlisted "}, wantOK: true, wantVisibility: "unlisted"},
		{name: "reject empty name", input: socialListInput{Name: "   "}, wantOK: false},
		{name: "reject oversized name", input: socialListInput{Name: string(make([]rune, 65))}, wantOK: false},
		{name: "reject oversized description", input: socialListInput{Name: "Valid", Description: string(make([]rune, 281))}, wantOK: false},
		{name: "reject unsupported visibility", input: socialListInput{Name: "Valid", Visibility: "followers"}, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input
			ok := normalizeSocialListInput(&got)
			if ok != tt.wantOK {
				t.Fatalf("normalizeSocialListInput()=%v want %v", ok, tt.wantOK)
			}
			if ok && got.Visibility != tt.wantVisibility {
				t.Fatalf("visibility=%q want %q", got.Visibility, tt.wantVisibility)
			}
		})
	}
}
