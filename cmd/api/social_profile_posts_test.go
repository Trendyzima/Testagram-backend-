package main

import "testing"

func TestParseProfileMediaOnly(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{name: "omitted", input: "", want: false},
		{name: "true", input: "true", want: true},
		{name: "false", input: "false", want: false},
		{name: "trimmed", input: " TRUE ", want: true},
		{name: "invalid", input: "images", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseProfileMediaOnly(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseProfileMediaOnly(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("parseProfileMediaOnly(%q) = %t, want %t", test.input, got, test.want)
			}
		})
	}
}
