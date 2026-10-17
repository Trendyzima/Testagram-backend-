package main

import (
	"testing"
	"time"
)

func TestParseQueueMonitorConfigs(t *testing.T) {
	got := parseQueueMonitorConfigs("federation:delivery, notifications:sender,invalid,media:transcode,federation:delivery")
	if len(got) != 3 {
		t.Fatalf("expected three unique valid queue/group pairs, got %#v", got)
	}
	if got[0] != (queueMonitorConfig{Queue: "federation", Group: "delivery"}) {
		t.Fatalf("unexpected first pair: %#v", got[0])
	}
	if got[1] != (queueMonitorConfig{Queue: "notifications", Group: "sender"}) {
		t.Fatalf("unexpected second pair: %#v", got[1])
	}
}

func TestStreamEntryAgeSeconds(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	age, ok := streamEntryAgeSeconds("1799999990000-0", now)
	if !ok || age != 10 {
		t.Fatalf("expected 10 second age, got age=%v ok=%v", age, ok)
	}
	age, ok = streamEntryAgeSeconds("1800000010000-0", now)
	if !ok || age != 0 {
		t.Fatalf("future stream IDs should clamp age to zero, got age=%v ok=%v", age, ok)
	}
	if _, ok := streamEntryAgeSeconds("not-an-id", now); ok {
		t.Fatal("invalid stream ID must be rejected")
	}
}