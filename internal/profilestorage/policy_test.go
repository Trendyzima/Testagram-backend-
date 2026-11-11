package profilestorage

import "testing"

func TestChooseTierUsesAppwriteUntilSafetyReserve(t *testing.T) {
	p := Policy{AppwriteCapacityBytes: 2_000, SafetyReserveBytes: 200}
	got, err := p.ChooseTier(1_000, 100, 699)
	if err != nil || got != TierAppwrite { t.Fatalf("tier=%q err=%v", got, err) }
	got, err = p.ChooseTier(1_000, 100, 701)
	if err != nil || got != TierInstabox { t.Fatalf("tier=%q err=%v", got, err) }
}

func TestChooseTierAccountsForConcurrentReservations(t *testing.T) {
	p := Policy{AppwriteCapacityBytes: 2_000, SafetyReserveBytes: 200}
	got, err := p.ChooseTier(1_500, 300, 1)
	if err != nil || got != TierInstabox { t.Fatalf("tier=%q err=%v", got, err) }
}

func TestChooseTierRejectsInvalidInputsAndOverflow(t *testing.T) {
	p := DefaultPolicy()
	for _, input := range [][3]int64{{-1,0,1},{0,-1,1},{0,0,0},{1<<63-1,1,1}} {
		if _, err := p.ChooseTier(input[0],input[1],input[2]); err == nil {
			t.Fatalf("expected error for %#v", input)
		}
	}
}

func TestDefaultPolicyLeavesReserve(t *testing.T) {
	p := DefaultPolicy()
	limit, err := p.AppwriteLimit()
	if err != nil { t.Fatal(err) }
	if limit != 1792*1024*1024 { t.Fatalf("limit=%d", limit) }
}
