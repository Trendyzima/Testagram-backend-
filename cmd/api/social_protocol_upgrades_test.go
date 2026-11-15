package main

import "testing"

func TestValidDefaultFeed(t *testing.T) {
	for _,feed:=range []string{"for-you","following","latest","media"} {
		if !validDefaultFeed(feed) { t.Fatalf("expected %s to be valid",feed) }
	}
	for _,feed:=range []string{"saved","admin","all","", "https://example.com"} {
		if validDefaultFeed(feed) { t.Fatalf("unexpected feed accepted: %s",feed) }
	}
}

func TestThreadQuoteHashesAreStableAndBindAllFields(t *testing.T) {
	key1,request1:=threadQuoteHashes("client-idempotency-key-123","11111111-1111-4111-8111-111111111111","my quote","public")
	key2,request2:=threadQuoteHashes("client-idempotency-key-123","11111111-1111-4111-8111-111111111111","my quote","public")
	if key1!=key2 || request1!=request2 { t.Fatal("identical quote requests must have stable hashes") }
	_,changedBody:=threadQuoteHashes("client-idempotency-key-123","11111111-1111-4111-8111-111111111111","changed quote","public")
	_,changedSource:=threadQuoteHashes("client-idempotency-key-123","22222222-2222-4222-8222-222222222222","my quote","public")
	_,changedVisibility:=threadQuoteHashes("client-idempotency-key-123","11111111-1111-4111-8111-111111111111","my quote","private")
	if request1==changedBody || request1==changedSource || request1==changedVisibility { t.Fatal("request hash must bind source, content and visibility") }
	if len(key1)!=64 || len(request1)!=64 { t.Fatal("hashes must be SHA-256 hex digests") }
}
