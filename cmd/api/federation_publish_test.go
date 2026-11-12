package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildFederationCreateActivity(t *testing.T) {
	createdAt := time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)
	activityID, payload, err := buildFederationCreateActivity(
		"https://federation.testagram.site/",
		"123e4567-e89b-42d3-a456-426614174001",
		"alice",
		"Alice",
		"<script>alert(1)</script>\nHello Fediverse",
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if activityID != "https://federation.testagram.site/activities/123e4567-e89b-42d3-a456-426614174001" {
		t.Fatalf("activity ID = %q", activityID)
	}
	var activity struct {
		ID     string         `json:"id"`
		Type   string         `json:"type"`
		Actor  string         `json:"actor"`
		To     []string       `json:"to"`
		Object map[string]any `json:"object"`
	}
	if err := json.Unmarshal(payload, &activity); err != nil {
		t.Fatal(err)
	}
	if activity.ID != activityID || activity.Type != "Create" || activity.Actor != "https://federation.testagram.site/users/alice" {
		t.Fatalf("invalid Create activity: %+v", activity)
	}
	if len(activity.To) != 1 || activity.To[0] != activityStreamsPublic {
		t.Fatalf("Create recipients = %#v", activity.To)
	}
	if activity.Object["id"] != "https://federation.testagram.site/objects/123e4567-e89b-42d3-a456-426614174001" ||
		activity.Object["type"] != "Note" ||
		activity.Object["attributedTo"] != activity.Actor {
		t.Fatalf("invalid embedded Note: %#v", activity.Object)
	}
	content, _ := activity.Object["content"].(string)
	if !strings.Contains(content, "&lt;script&gt;") || !strings.Contains(content, "<br />Hello Fediverse") || strings.Contains(content, "<script>") {
		t.Fatalf("Note content was not safely escaped: %q", content)
	}
}

func TestNormalizeFederationPublicBaseURLRejectsUnsafeOrigins(t *testing.T) {
	for _, value := range []string{
		"http://federation.testagram.site",
		"https://user:pass@federation.testagram.site",
		"https://federation.testagram.site/path",
		"https://federation.testagram.site?token=secret",
		"https://federation.testagram.site#fragment",
	} {
		if _, err := normalizeFederationPublicBaseURL(value); err == nil {
			t.Errorf("unsafe Federation origin accepted: %q", value)
		}
	}
	if got, err := normalizeFederationPublicBaseURL("https://federation.testagram.site/"); err != nil || got != "https://federation.testagram.site" {
		t.Fatalf("valid origin normalization = %q, %v", got, err)
	}
}
