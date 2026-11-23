package main

import "testing"

func TestTrendMomentum(t *testing.T) {
	cases := []struct {
		name string
		current int64
		previous int64
		want float64
	}{
		{name: "new topic", current: 5, previous: 0, want: 1},
		{name: "doubling", current: 10, previous: 5, want: 0.5},
		{name: "steady", current: 8, previous: 8, want: 0},
		{name: "declining", current: 4, previous: 8, want: 0},
		{name: "empty", current: 0, previous: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trendMomentum(tc.current, tc.previous); got != tc.want {
				t.Fatalf("trendMomentum(%d, %d) = %v, want %v", tc.current, tc.previous, got, tc.want)
			}
		})
	}
}

func TestTrendReasonsIdentifyOnlyAcceleratingTopics(t *testing.T) {
	if got := trendReasons(0.6); len(got) != 1 || got[0] != "accelerating" {
		t.Fatalf("trendReasons(0.6) = %#v, want accelerating", got)
	}
	if got := trendReasons(0.59); len(got) != 0 {
		t.Fatalf("trendReasons(0.59) = %#v, want no reasons", got)
	}
}
