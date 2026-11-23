package main

import (
	"testing"
	"time"
)

func TestParseSearchFilters(t *testing.T) {
	filters, err := parseSearchFilters(map[string][]string{
		"from":       {"@Creator_01"},
		"since":      {"2026-11-01"},
		"until":      {"2026-11-23"},
		"language":   {"en-KE"},
		"sort":       {"popular"},
		"media_only": {"true"},
		"video_only": {"false"},
	})
	if err != nil {
		t.Fatalf("parseSearchFilters: %v", err)
	}
	if filters.From != "Creator_01" || filters.Language != "en-ke" ||
		filters.Sort != "popular" || !filters.MediaOnly || filters.VideoOnly {
		t.Fatalf("unexpected parsed filters: %+v", filters)
	}
	if filters.Since == nil || filters.Until == nil {
		t.Fatal("expected both date filters")
	}
	if got, want := *filters.Since, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("since = %s, want %s", got, want)
	}
	if got, want := *filters.Until, time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("until exclusive bound = %s, want %s", got, want)
	}
}

func TestParseSearchFiltersDefaults(t *testing.T) {
	filters, err := parseSearchFilters(map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if filters.Sort != "latest" || filters.MediaOnly || filters.VideoOnly ||
		filters.From != "" || filters.Since != nil || filters.Until != nil || filters.Language != "" {
		t.Fatalf("unexpected defaults: %+v", filters)
	}
}

func TestParseSearchFiltersRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		values map[string][]string
	}{
		{name: "invalid username", values: map[string][]string{"from": {"bad/name"}}},
		{name: "invalid date", values: map[string][]string{"since": {"yesterday"}}},
		{name: "reversed dates", values: map[string][]string{"since": {"2026-11-24"}, "until": {"2026-11-23"}}},
		{name: "invalid language", values: map[string][]string{"language": {"en_KE"}}},
		{name: "invalid sort", values: map[string][]string{"sort": {"random"}}},
		{name: "invalid boolean", values: map[string][]string{"media_only": {"yes"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSearchFilters(tc.values); err == nil {
				t.Fatal("expected invalid search filters to be rejected")
			}
		})
	}
}
