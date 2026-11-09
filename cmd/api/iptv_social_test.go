package main

import (
	"encoding/json"
	"testing"
)

func TestIPTVChannelMetadataDropsStreamCredentials(t *testing.T) {
	payload := map[string]json.RawMessage{
		"channel": json.RawMessage(`{"id":"channel-1","name":"News","logo":"https://cdn.example/logo.png","country":"KE","countryCode":"KE","categories":["news"],"streamUrl":"https://stream.example/live.m3u8?token=secret","headers":{"Authorization":"Bearer secret"},"apiKey":"secret"}`),
	}
	id, channel, err := iptvChannel(payload)
	if err != nil {
		t.Fatalf("iptvChannel returned error: %v", err)
	}
	if id != "channel-1" {
		t.Fatalf("unexpected channel id %q", id)
	}
	for _, forbidden := range []string{"streamUrl", "headers", "apiKey", "authorization", "token"} {
		if _, ok := channel[forbidden]; ok {
			t.Fatalf("sensitive channel field %q was retained", forbidden)
		}
	}
	if channel["name"] != "News" || channel["logo"] != "https://cdn.example/logo.png" {
		t.Fatalf("safe display metadata was not retained: %#v", channel)
	}
	if channel["country_code"] != "KE" {
		t.Fatalf("country_code compatibility alias missing: %#v", channel)
	}
	if channel["channel_name"] != "News" || channel["channel_logo"] != "https://cdn.example/logo.png" {
		t.Fatalf("watch-history display aliases missing: %#v", channel)
	}
}
