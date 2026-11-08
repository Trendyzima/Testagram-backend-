package main

import "testing"

func TestMoneyAmount(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
		ok    bool
	}{
		{name: "KES decimal string", input: "125.50", want: "125.50", ok: true},
		{name: "whole amount", input: float64(10), want: "10.00", ok: true},
		{name: "maximum amount", input: "10000.00", want: "10000.00", ok: true},
		{name: "zero rejected", input: "0", ok: false},
		{name: "negative rejected", input: "-1.00", ok: false},
		{name: "exponent rejected", input: "1e2", ok: false},
		{name: "fractional cent rejected", input: float64(1.001), ok: false},
		{name: "over maximum rejected", input: "10000.01", ok: false},
		{name: "comma rejected", input: "1,000.00", ok: false},
		{name: "unsupported type rejected", input: int(10), ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := moneyAmount(tt.input)
			if ok != tt.ok {
				t.Fatalf("moneyAmount(%v) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("moneyAmount(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeMpesaPhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "0712345678", want: "254712345678", ok: true},
		{input: "712345678", want: "254712345678", ok: true},
		{input: "+254712345678", want: "254712345678", ok: true},
		{input: "0112345678", want: "254112345678", ok: true},
		{input: "254612345678", ok: false},
		{input: "12345", ok: false},
		{input: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := normalizeMpesaPhone(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeMpesaPhone(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("normalizeMpesaPhone(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
