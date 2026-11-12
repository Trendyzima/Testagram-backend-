package main

import "testing"

func TestValidProfileWebsite(t *testing.T) {
	for _, value := range []string{"", "https://example.com", "http://example.com/path"} {
		if !validProfileWebsite(value) {
			t.Errorf("validProfileWebsite(%q) = false", value)
		}
	}
	for _, value := range []string{"javascript:alert(1)", "//example.com", "https://user:pass@example.com", "https://example.com#fragment", "not a url"} {
		if validProfileWebsite(value) {
			t.Errorf("validProfileWebsite(%q) = true", value)
		}
	}
}

func TestValidProfileMediaKey(t *testing.T) {
	valid := "users/user-123/0123456789abcdef0123456789abcdef"
	if !validProfileMediaKey(valid) || !validProfileMediaKey("") {
		t.Fatal("valid profile media key rejected")
	}
	for _, value := range []string{"users/user-123/not-a-key", "users/../0123456789abcdef0123456789abcdef", "https://example.com/image.png"} {
		if validProfileMediaKey(value) {
			t.Errorf("validProfileMediaKey(%q) = true", value)
		}
	}
}

func TestValidProfileSocialLinks(t *testing.T) {
	if !validProfileSocialLinks(map[string]string{"instagram": "@testagram", "linkedin": "https://linkedin.com/in/test"}) {
		t.Fatal("valid social links rejected")
	}
	if validProfileSocialLinks(map[string]string{"javascript": "alert(1)"}) {
		t.Fatal("unknown social link accepted")
	}
}
