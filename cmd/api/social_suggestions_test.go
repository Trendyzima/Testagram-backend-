package main

import "testing"

func TestParseSocialSuggestionLimit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "default", input: "", want: 8},
		{name: "trimmed", input: " 12 ", want: 12},
		{name: "minimum", input: "1", want: 1},
		{name: "maximum", input: "20", want: 20},
		{name: "zero", input: "0", wantErr: true},
		{name: "too large", input: "21", wantErr: true},
		{name: "invalid", input: "many", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSocialSuggestionLimit(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseSocialSuggestionLimit(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("parseSocialSuggestionLimit(%q) = %d, want %d", test.input, got, test.want)
			}
		})
	}
}

func TestSocialSuggestionReason(t *testing.T) {
	tests := []struct {
		mutual, recent int64
		want           string
	}{
		{mutual: 2, recent: 8, want: "followed_by_people_you_follow"},
		{mutual: 0, recent: 3, want: "active_in_testagram"},
		{mutual: 0, recent: 0, want: "popular_in_testagram"},
	}
	for _, test := range tests {
		if got := socialSuggestionReason(test.mutual, test.recent); got != test.want {
			t.Errorf("socialSuggestionReason(%d,%d) = %q, want %q", test.mutual, test.recent, got, test.want)
		}
	}
}
