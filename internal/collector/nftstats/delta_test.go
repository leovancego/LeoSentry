package nftstats

import "testing"

func TestDeltaTracker(t *testing.T) {
	a := FlowPair{Device: [4]byte{192, 168, 1, 10}, Target: [4]byte{1, 1, 1, 1}}
	b := FlowPair{Device: [4]byte{192, 168, 1, 11}, Target: [4]byte{2, 2, 2, 2}}
	d := newDeltaTracker()

	collect := func(cur map[FlowPair]uint64) map[FlowPair]uint64 {
		out := map[FlowPair]uint64{}
		d.update(cur, func(k FlowPair, v uint64) { out[k] = v })
		return out
	}

	got := collect(map[FlowPair]uint64{a: 100})
	if got[a] != 100 {
		t.Fatalf("new element delta = %d", got[a])
	}
	got = collect(map[FlowPair]uint64{a: 150, b: 10})
	if got[a] != 50 || got[b] != 10 {
		t.Fatalf("second read = %v", got)
	}
	got = collect(map[FlowPair]uint64{a: 150, b: 10})
	if len(got) != 0 {
		t.Fatalf("unchanged counters produced deltas: %v", got)
	}
	// a 超时后被重新创建，计数回落
	got = collect(map[FlowPair]uint64{a: 20})
	if got[a] != 20 {
		t.Fatalf("recreated element delta = %d", got[a])
	}
	if _, ok := d.last[b]; ok {
		t.Fatal("expired element should be forgotten")
	}
}

func TestDecodeKey(t *testing.T) {
	p, ok := decodeKey([]byte{192, 168, 1, 10, 8, 8, 8, 8})
	if !ok || p.DeviceAddr().String() != "192.168.1.10" || p.TargetAddr().String() != "8.8.8.8" {
		t.Fatalf("decode = %v %v", p, ok)
	}
	if _, ok := decodeKey([]byte{1, 2, 3}); ok {
		t.Fatal("short key should fail")
	}
}
