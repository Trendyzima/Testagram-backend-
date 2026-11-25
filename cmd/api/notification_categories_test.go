package main

import "testing"

func TestParseNotificationCategory(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"", "all", true},
		{" ALL ", "all", true},
		{"mentions", "mentions", true},
		{"wallet", "wallet", true},
		{"messages", "messages", true},
		{"unknown", "", false},
		{"all;wallet", "", false},
	}
	for _, test := range tests {
		got, err := parseNotificationCategory(test.input)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("parseNotificationCategory(%q) = (%q, %v), want (%q, ok=%v)", test.input, got, err, test.want, test.ok)
		}
	}
}

func TestNotificationCategory(t *testing.T) {
	tests := map[string]string{
		"mention":                     "mentions",
		"social.mention":              "mentions",
		"social.reactions.aggregated": "likes",
		"social.follow":               "follows",
		"social.follow_request":       "follows",
		"social.reply":                "replies",
		"social.repost":               "reposts",
		"message.new":                 "messages",
		"wallet.transfer.received":    "wallet",
		"security.alert":              "system",
	}
	for kind, want := range tests {
		if got := notificationCategory(kind); got != want {
			t.Errorf("notificationCategory(%q) = %q, want %q", kind, got, want)
		}
	}
}
