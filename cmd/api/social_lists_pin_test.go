package main

import "testing"

func TestSocialListPinCapacityExceeded(t *testing.T) {
	tests := []struct {
		name            string
		count           int64
		currentlyPinned bool
		requestedPinned bool
		wantExceeded    bool
	}{
		{name: "pin below limit", count: 4, requestedPinned: true, wantExceeded: false},
		{name: "pin at limit", count: 5, requestedPinned: true, wantExceeded: true},
		{name: "already pinned remains idempotent at limit", count: 5, currentlyPinned: true, requestedPinned: true, wantExceeded: false},
		{name: "unpin at limit", count: 5, currentlyPinned: true, requestedPinned: false, wantExceeded: false},
		{name: "leave unpinned", count: 0, requestedPinned: false, wantExceeded: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := socialListPinCapacityExceeded(tt.count, tt.currentlyPinned, tt.requestedPinned); got != tt.wantExceeded {
				t.Fatalf("socialListPinCapacityExceeded(%d, %v, %v)=%v want %v", tt.count, tt.currentlyPinned, tt.requestedPinned, got, tt.wantExceeded)
			}
		})
	}
}
