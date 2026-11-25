package main

import "testing"

func TestValidNotificationStreamOrigin(t *testing.T) {
	for _, input := range []string{
		"http://notification:8090",
		"http://notification.internal:8090",
		"https://notifications.testagram.site",
	} {
		if _, err := validNotificationStreamOrigin(input); err != nil {
			t.Fatalf("expected trusted notification origin %q, got %v", input, err)
		}
	}
}

func TestValidNotificationStreamOriginRejectsUntrustedHTTPAndURLParts(t *testing.T) {
	for _, input := range []string{
		"http://notifications.example.com",
		"ftp://notification:8090",
		"http://user:password@notification:8090",
		"http://notification:8090/path",
		"http://notification:8090?token=secret",
		"http://notification:8090#fragment",
		"",
	} {
		if _, err := validNotificationStreamOrigin(input); err == nil {
			t.Fatalf("expected notification origin %q to be rejected", input)
		}
	}
}
