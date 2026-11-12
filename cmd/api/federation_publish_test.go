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
	rawTags, ok := activity.Object["tag"].([]any)
	if !ok || len(rawTags) != 2 {
		t.Fatalf("ActivityPub Note tags = %#v, want two unique Hashtag objects", activity.Object["tag"])
	}
	gotTags := map[string]bool{}
	for _, raw := range rawTags {
		tag, ok := raw.(map[string]any)
		if !ok || tag["type"] != "Hashtag" {
			t.Fatalf("invalid ActivityPub Hashtag object: %#v", raw)
		}
		gotTags[tag["name"].(string)] = true
		if !strings.HasPrefix(tag["href"].(string), "https://testagram.site/hashtag/") {
			t.Fatalf("hashtag href is not a Testagram topic URL: %#v", tag)
		}
	}
	if !gotTags["#Testagram"] || !gotTags["#Fediverse"] {
		t.Fatalf("ActivityPub Note omitted expected hashtags: %#v", gotTags)
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
