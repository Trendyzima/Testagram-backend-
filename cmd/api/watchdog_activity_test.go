package main

import (
	"strings"
	"testing"
	"time"
)

func TestWatchdogBucketKeyIsStableAndContainsNoIdentity(t *testing.T) {
	at:=time.Date(2026,11,15,17,42,0,0,time.FixedZone("EAT",3*60*60))
	a:=watchdogBucketKey("feed_open",at)
	b:=watchdogBucketKey("feed_open",at.Add(4*time.Minute))
	c:=watchdogBucketKey("feed_open",at.Add(5*time.Minute))
	if a!=b { t.Fatalf("same bucket should deduplicate: %q != %q",a,b) }
	if a==c { t.Fatalf("next bucket should allow a fresh wake-up: %q",a) }
	if strings.Contains(a,"@") || strings.Contains(a,"user") || strings.Contains(a,"email") { t.Fatalf("key must not contain identity data: %q",a) }
	if len(a)<16 || len(a)>128 { t.Fatalf("invalid key length: %d",len(a)) }
}

func TestWatchdogEventAllowlist(t *testing.T) {
	for _,event:=range []string{"login","feed_open","post_publish","media_upload","media_view","federation_interaction"} {
		if !validWatchdogEvent(event) { t.Fatalf("expected supported event %s",event) }
	}
	for _,event:=range []string{"delete_account","run_command","", "postgres"} {
		if validWatchdogEvent(event) { t.Fatalf("unexpected event accepted: %s",event) }
	}
}
