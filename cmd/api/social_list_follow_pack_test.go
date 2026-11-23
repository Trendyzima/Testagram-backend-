package main

import "testing"

func TestParseSocialListFollowLimit(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "default", input: "", want: 50},
		{name: "bounded", input: "25", want: 25},
		{name: "maximum", input: "50", want: 50},
		{name: "zero rejected", input: "0", wantErr: true},
		{name: "over maximum rejected", input: "51", wantErr: true},
		{name: "non numeric rejected", input: "many", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSocialListFollowLimit(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseSocialListFollowLimit(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("parseSocialListFollowLimit(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
