package main

import (
	"testing"
	"time"
)

func TestAdCampaignStatusAfterReview(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tests := []struct {
		name           string
		decision       string
		paymentStatus  string
		previousStatus string
		endsAt         *time.Time
		want           string
	}{
		{name: "rejection is terminal", decision: "rejected", paymentStatus: "funded", previousStatus: "active", want: "rejected"},
		{name: "approval without funding waits for payment", decision: "approved", paymentStatus: "unpaid", previousStatus: "pending_review", want: "pending_payment"},
		{name: "funded campaign activates", decision: "approved", paymentStatus: "funded", previousStatus: "pending_review", want: "active"},
		{name: "paused campaign remains paused", decision: "approved", paymentStatus: "funded", previousStatus: "paused", want: "paused"},
		{name: "expired campaign completes", decision: "approved", paymentStatus: "funded", previousStatus: "pending_review", endsAt: &past, want: "completed"},
		{name: "future campaign activates", decision: "approved", paymentStatus: "funded", previousStatus: "pending_review", endsAt: &future, want: "active"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := adCampaignStatusAfterReview(test.decision, test.paymentStatus, test.previousStatus, test.endsAt, now)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}
