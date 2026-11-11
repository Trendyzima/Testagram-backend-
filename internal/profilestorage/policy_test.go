package profilestorage

import (
	"math"
	"testing"
)

func TestSelectTierUsesAppwriteWhileWithinSafeCapacity(t *testing.T) {
	tier, err := SelectTier(Capacity{LimitBytes: 2_000, UsedBytes: 1_000, SafetyReserveBytes: 200}, 500)
	if err != nil || tier != TierAppwrite {
		t.Fatalf("tier=%q err=%v", tier, err)
	}
}

func TestSelectTierOverflowsBeforeAppwriteQuotaIsExhausted(t *testing.T) {
	capacity := Capacity{LimitBytes: 2_000, UsedBytes: 1_600, ReservedBytes: 100, SafetyReserveBytes: 200}
	tier, err := SelectTier(capacity, 101)
	if err != nil || tier != TierInstabox {
		t.Fatalf("tier=%q err=%v", tier, err)
	}
}

func TestSelectTierAccountsForOutstandingReservations(t *testing.T) {
	tier, err := SelectTier(Capacity{LimitBytes: 2_000, UsedBytes: 1_000, ReservedBytes: 900, SafetyReserveBytes: 100}, 1)
	if err != nil || tier != TierInstabox {
		t.Fatalf("tier=%q err=%v", tier, err)
	}
}

func TestSelectTierRejectsInvalidCapacity(t *testing.T) {
	cases := []Capacity{
		{LimitBytes: 0},
		{LimitBytes: 100, UsedBytes: -1},
		{LimitBytes: 100, UsedBytes: 101},
		{LimitBytes: 100, ReservedBytes: -1},
		{LimitBytes: 100, SafetyReserveBytes: -1},
		{LimitBytes: 100, SafetyReserveBytes: 100},
		{LimitBytes: 100, UsedBytes: math.MaxInt64, ReservedBytes: 1},
	}
	for _, capacity := range cases {
		if _, err := SelectTier(capacity, 1); err == nil {
			t.Fatalf("expected invalid capacity for %+v", capacity)
		}
	}
}

func TestSelectTierRejectsNonPositiveIncomingSize(t *testing.T) {
	capacity := Capacity{LimitBytes: 1000, SafetyReserveBytes: 100}
	for _, incoming := range []int64{0, -1} {
		if _, err := SelectTier(capacity, incoming); err == nil {
			t.Fatalf("expected invalid incoming size %d", incoming)
		}
	}
}
