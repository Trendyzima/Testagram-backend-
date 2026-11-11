package main

import (
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

func TestOrderHomeFeedByRankingRequiresCompleteCandidateCoverage(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	posts := []homeFeedPost{
		{ID: "post-1", CreatedAt: now},
		{ID: "post-2", CreatedAt: now.Add(-time.Minute)},
	}
	ranked := []algorithmclient.RankedItem{
		{ID: "post-2", Score: 0.9, Reasons: []string{"quality-signal"}},
		{ID: "post-1", Score: 0.5, Reasons: []string{"recent"}},
	}
	ordered, complete := orderHomeFeedByRanking(posts, ranked)
	if !complete {
		t.Fatal("complete ranking response was rejected")
	}
	if len(ordered) != 2 || ordered[0].ID != "post-2" || ordered[1].ID != "post-1" {
		t.Fatalf("ranking order = %#v", ordered)
	}
	if ordered[0].RankingScore != 0.9 {
		t.Fatalf("ranking metadata was not applied: %#v", ordered[0])
	}
}

func TestOrderHomeFeedByRankingFallsBackForPartialOrInvalidResponses(t *testing.T) {
	posts := []homeFeedPost{{ID: "post-1"}, {ID: "post-2"}}
	cases := map[string][]algorithmclient.RankedItem{
		"empty": nil,
		"partial": {{ID: "post-1", Score: 0.8}},
		"unknown ID": {{ID: "post-1", Score: 0.8}, {ID: "foreign-post", Score: 0.7}},
	}
	for name, ranked := range cases {
		t.Run(name, func(t *testing.T) {
			got, complete := orderHomeFeedByRanking(posts, ranked)
			if complete {
				t.Fatal("incomplete or invalid ranking response was accepted")
			}
			if len(got) != len(posts) || got[0].ID != "post-1" || got[1].ID != "post-2" {
				t.Fatalf("chronological fallback was not preserved: %#v", got)
			}
		})
	}
}

func TestOrderHomeFeedByRankingRejectsDuplicateCandidateIDs(t *testing.T) {
	posts := []homeFeedPost{{ID: "post-1"}, {ID: "post-1"}}
	ranked := []algorithmclient.RankedItem{{ID: "post-1"}, {ID: "post-1"}}
	got, complete := orderHomeFeedByRanking(posts, ranked)
	if complete || len(got) != 2 {
		t.Fatalf("duplicate candidates must retain original page: %#v, complete=%v", got, complete)
	}
}
