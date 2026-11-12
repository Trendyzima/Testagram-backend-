package main

import "testing"

func TestNormalizeMpesaPhone(t *testing.T) {
	valid := map[string]string{
		"0712345678": "+254712345678",
		"0112345678": "+254112345678",
		"254712345678": "+254712345678",
		"+254112345678": "+254112345678",
		"712345678": "+254712345678",
		" 0712 345 678 ": "+254712345678",
	}
	for input, expected := range valid {
		got, ok := normalizeMpesaPhone(input)
		if !ok || got != expected {
			t.Errorf("normalizeMpesaPhone(%q) = (%q, %v), want (%q, true)", input, got, ok, expected)
		}
	}
	invalid := []string{
		"", "0201234567", "071234567", "07123456789",
		"+254201234567", "25471234567", "2547123456789",
	}
	for _, input := range invalid {
		if got, ok := normalizeMpesaPhone(input); ok {
			t.Errorf("normalizeMpesaPhone(%q) = (%q, true), want invalid", input, got)
		}
	}
}
