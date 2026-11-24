package main

import (
	"bytes"
	"testing"
)

func TestNativeBounceIngressMACBindsRequest(t *testing.T) {
	secret := "test-only-secret-with-at-least-thirty-two-bytes"
	body := []byte("original RFC 5322 bounce message")
	timestamp := "1795600000"
	recipient := "b+abcdefghijklmnopqrstuvwxyzABCDEF@bounces.testagram.site"
	want := nativeBounceIngressMAC(secret, timestamp, recipient, body)
	if len(want) != 32 { t.Fatalf("expected SHA-256 HMAC, got %d bytes", len(want)) }
	if got := nativeBounceIngressMAC(secret, timestamp, recipient, body); !bytes.Equal(got, want) { t.Fatal("same request did not produce a stable authentication tag") }
	if got := nativeBounceIngressMAC(secret, timestamp, "b+differenttoken@bounces.testagram.site", body); bytes.Equal(got, want) { t.Fatal("recipient was not authenticated") }
	if got := nativeBounceIngressMAC(secret, "1795600001", recipient, body); bytes.Equal(got, want) { t.Fatal("timestamp was not authenticated") }
	if got := nativeBounceIngressMAC(secret, timestamp, recipient, []byte("modified body")); bytes.Equal(got, want) { t.Fatal("raw message body was not authenticated") }
	if got := nativeBounceIngressMAC(secret+"x", timestamp, recipient, body); bytes.Equal(got, want) { t.Fatal("shared secret was not authenticated") }
}
