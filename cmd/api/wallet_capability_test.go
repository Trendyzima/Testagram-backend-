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

func TestValidWalletUsername(t *testing.T) {
	valid := []string{"trendyzima", "test.user", "user_123", "AbC"}
	for _, username := range valid {
		if !validWalletUsername(username) {
			t.Errorf("validWalletUsername(%q) = false, want true", username)
		}
	}
	invalid := []string{"", "a", "user name", "@username", "user/name", "user!name", "this_username_is_far_too_long_for_the_limit"}
	for _, username := range invalid {
		if validWalletUsername(username) {
			t.Errorf("validWalletUsername(%q) = true, want false", username)
		}
	}
}

func TestMpesaTopupIdempotencyBindsOriginalPayload(t *testing.T) {
	if !mpesaTopupRequestMatches("500.00", "254712345678", "KES", 500, "254712345678", "KES") {
		t.Fatal("same M-Pesa payload should be an idempotent retry")
	}
	tests := []struct {
		name, amount, phone, currency     string
		requestedAmount                   float64
		requestedPhone, requestedCurrency string
	}{
		{name: "different amount", amount: "500.00", phone: "254712345678", currency: "KES", requestedAmount: 501, requestedPhone: "254712345678", requestedCurrency: "KES"},
		{name: "different phone", amount: "500.00", phone: "254712345678", currency: "KES", requestedAmount: 500, requestedPhone: "254711111111", requestedCurrency: "KES"},
		{name: "different currency", amount: "500.00", phone: "254712345678", currency: "KES", requestedAmount: 500, requestedPhone: "254712345678", requestedCurrency: "USD"},
		{name: "malformed stored amount", amount: "not-money", phone: "254712345678", currency: "KES", requestedAmount: 500, requestedPhone: "254712345678", requestedCurrency: "KES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if mpesaTopupRequestMatches(tt.amount, tt.phone, tt.currency, tt.requestedAmount, tt.requestedPhone, tt.requestedCurrency) {
				t.Fatal("different or malformed payload must not reuse an idempotency key")
			}
		})
	}
}
