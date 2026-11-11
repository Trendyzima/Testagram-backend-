package profilestorage

import "testing"

func TestSelectTierUsesAppwriteWhileWithinSafeCapacity(t *testing.T) {
	tier, err := SelectTier(Capacity{LimitBytes: 2_000, UsedBytes: 1_000, SafetyReserveBytes: 200}, 500)
	if err != nil || tier != TierAppwrite { t.Fatalf("tier=%q err=%v", tier, err) }
}

func TestSelectTierOverflowsBeforeAppwriteQuotaIsExhausted(t *testing.T) {
	capacity := Capacity{LimitBytes: 2_000, UsedBytes: 1_600, ReservedBytes: 100, SafetyReserveBytes: 200}
	tier, err := SelectTier(capacity, 101)
	if err != nil || tier != TierInstabox { t.Fatalf("tier=%q err=%v", tier, err) }
}

func TestSelectTierAccountsForOutstandingReservations(t *testing.T) {
	tier, err := SelectTier(Capacity{LimitBytes: 2_000, UsedBytes: 1_000, ReservedBytes: 900, SafetyReserveBytes: 100}, 1)
	if err != nil || tier != TierInstabox { t.Fatalf("tier=%q err=%v", tier, err) }
}

func TestSelectTierRejectsInvalidCapacity(t *testing.T) {
	if _, err := SelectTier(Capacity{LimitBytes: 100, UsedBytes: 101}, 1); err == nil {
		t.Fatal("expected invalid capacity")
	}
	if _, err := SelectTier(Capacity{LimitBytes: 100, SafetyReserveBytes: 100}, 1); err == nil {
		t.Fatal("expected invalid safety reserve")
	}
}
