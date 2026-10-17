package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDurableJobValidation(t *testing.T) {
	for _, tc := range []struct{name string; queue string; payload string; want bool}{
		{"object", "feed", `{"post_id":"p1"}`, true},
		{"array", "feed", `[1,2]`, false},
		{"null", "feed", `null`, false},
		{"invalid json", "feed", `{"x":`, false},
		{"bad queue", "feed/name", `{"x":1}`, false},
		{"empty object", "feed", `{}`, true},
		{"too large", "feed", `{"x":"`+strings.Repeat("a", 512<<10)+`"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := validJobQueue(tc.queue) && validJSONObject(json.RawMessage(tc.payload))
			if got != tc.want { t.Fatalf("got %v, want %v",got,tc.want) }
		})
	}
}

func TestValidUUID(t *testing.T) {
	for _, tc := range []struct{v string; want bool}{
		{"d9428888-122b-4a7a-b6d2-9c2b2f3a1f1a",true},
		{"D9428888-122B-4A7A-B6D2-9C2B2F3A1F1A",true},
		{"not-a-uuid",false},
		{"d9428888122b4a7ab6d29c2b2f3a1f1a",false},
	} { if got:=validUUID(tc.v);got!=tc.want { t.Errorf("validUUID(%q)=%v want %v",tc.v,got,tc.want) } }
}
