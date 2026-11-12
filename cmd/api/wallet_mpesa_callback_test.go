package main

import "testing"

func TestMpesaResultCodeMatches(t *testing.T) {
	tests := []struct {
		name string
		result map[string]any
		expected int
		want bool
	}{
		{"success matches", map[string]any{"ResponseCode": "0", "ResultCode": "0"}, 0, true},
		{"provider failure matches", map[string]any{"ResponseCode": "0", "ResultCode": "1032"}, 1032, true},
		{"callback cannot invent failure", map[string]any{"ResponseCode": "0", "ResultCode": "0"}, 1032, false},
		{"callback cannot invent success", map[string]any{"ResponseCode": "0", "ResultCode": "1032"}, 0, false},
		{"provider query rejected", map[string]any{"ResponseCode": "1", "ResultCode": "1032"}, 1032, false},
		{"missing result code", map[string]any{"ResponseCode": "0"}, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mpesaResultCodeMatches(test.result, test.expected); got != test.want {
				t.Fatalf("mpesaResultCodeMatches() = %v, want %v", got, test.want)
			}
		})
	}
}
