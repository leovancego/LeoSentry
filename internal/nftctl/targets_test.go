package nftctl

import (
	"net/netip"
	"testing"
)

func TestDiffSkipsUnchangedSets(t *testing.T) {
	a := netip.MustParseAddr("192.168.1.10")
	b := netip.MustParseAddr("192.168.1.11")
	game := netip.MustParseAddr("1.2.3.4")
	base := Targets{BlockAll: []netip.Addr{b, a}, GameDest: []netip.Addr{game}}
	ops, changed := Diff(base, Targets{BlockAll: []netip.Addr{a, a, b}, GameDest: []netip.Addr{game}})
	if changed || len(ops) != 0 {
		t.Fatalf("identical sets changed: %+v", ops)
	}
	ops, changed = Diff(base, Targets{BlockGame: []netip.Addr{a}, GameDest: []netip.Addr{game}})
	if !changed {
		t.Fatal("expected a change")
	}
	got := map[string]SetOp{}
	for _, op := range ops {
		got[op.Name] = op
	}
	if len(got[SetBlockAll].Del) != 2 || len(got[SetBlockAll].Add) != 0 {
		t.Fatalf("block_all = %+v", got[SetBlockAll])
	}
	if len(got[SetBlockGame].Add) != 1 || got[SetBlockGame].Add[0] != a {
		t.Fatalf("block_game = %+v", got[SetBlockGame])
	}
	if _, ok := got[SetGameDst]; ok {
		t.Fatal("unchanged destination set should not be rewritten")
	}
	ops, changed = Diff(Targets{}, Targets{})
	if changed {
		t.Fatal("empty plan should not write nftables")
	}
}
