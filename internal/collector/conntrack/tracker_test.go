package conntrack

import (
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/model"
)

type fakeDevices map[netip.Addr]model.Device

func (f fakeDevices) Lookup(ip netip.Addr) (model.Device, bool) {
	d, ok := f[ip]
	return d, ok
}

func TestParseProcLine(t *testing.T) {
	line := "ipv4     2 tcp      6 7440 ESTABLISHED src=192.168.1.10 dst=1.2.3.4 sport=51234 dport=443 packets=3 bytes=180 src=1.2.3.4 dst=100.64.0.2 sport=443 dport=51234 packets=2 bytes=120 [ASSURED] mark=0 zone=0 use=2"
	k, ok := parseProcLine(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if netip.AddrFrom4(k.src).String() != "192.168.1.10" || netip.AddrFrom4(k.dst).String() != "1.2.3.4" ||
		k.sport != 51234 || k.dport != 443 || k.proto != 6 {
		t.Fatalf("key = %+v", k)
	}
	if _, ok := parseProcLine("ipv6     10 udp      17 30 src=fd00::1 dst=fd00::2 sport=1 dport=2"); ok {
		t.Fatal("ipv6 should be skipped")
	}
}

func TestTrackerDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nf_conntrack")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const (
		a = "ipv4 2 tcp 6 100 ESTABLISHED src=192.168.1.10 dst=1.1.1.1 sport=1000 dport=443 src=1.1.1.1 dst=100.64.0.2 sport=443 dport=1000\n"
		b = "ipv4 2 udp 17 30 src=192.168.1.10 dst=2.2.2.2 sport=2000 dport=53 src=2.2.2.2 dst=100.64.0.2 sport=53 dport=2000\n"
		w = "ipv4 2 tcp 6 100 ESTABLISHED src=8.8.8.8 dst=100.64.0.2 sport=3000 dport=22 src=100.64.0.2 dst=8.8.8.8 sport=22 dport=3000\n"
		r = "ipv4 2 udp 17 30 src=192.168.1.1 dst=192.168.1.10 sport=5353 dport=5353 src=192.168.1.10 dst=192.168.1.1 sport=5353 dport=5353\n"
	)
	write("")
	src, err := NewProcfsSource(path)
	if err != nil {
		t.Fatal(err)
	}
	dev := netip.MustParseAddr("192.168.1.10")
	tr := NewTracker(src, time.Second, []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")},
		fakeDevices{dev: {MAC: "aa:bb:cc:00:00:01", IP: dev, Hostname: "kid"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.Ignore(netip.MustParseAddr("192.168.1.1"))

	now := time.Now()
	write(a + w + r)
	tr.tick(now)
	acts := tr.Activities()
	if len(acts) != 1 || acts[0].Flows != 1 || acts[0].NewFlows != 1 || acts[0].MAC != "aa:bb:cc:00:00:01" {
		t.Fatalf("first tick = %+v", acts)
	}

	write(b)
	tr.tick(now.Add(time.Second))
	acts = tr.Activities()
	if acts[0].Flows != 1 || acts[0].NewFlows != 1 || acts[0].EndedFlows != 1 || !acts[0].Online {
		t.Fatalf("second tick = %+v", acts[0])
	}

	write("")
	tr.tick(now.Add(2 * time.Second))
	acts = tr.Activities()
	if acts[0].Online || acts[0].EndedFlows != 1 || len(tr.prev) != 0 {
		t.Fatalf("third tick = %+v", acts[0])
	}
}
