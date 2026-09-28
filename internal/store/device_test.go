package store

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/leo/leosentry/internal/model"
)

func openTestRegistry(t *testing.T, persist time.Duration) *Registry {
	t.Helper()
	r, err := OpenRegistry(context.Background(), RegistryOptions{
		Path:         filepath.Join(t.TempDir(), "devices.db"),
		PersistEvery: persist,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestRegistryObserveAndRename(t *testing.T) {
	ctx := context.Background()
	r := openTestRegistry(t, time.Nanosecond)
	mac := "aa:bb:cc:00:00:01"
	ip1 := netip.MustParseAddr("192.168.1.10")
	ip2 := netip.MustParseAddr("192.168.1.20")
	t1 := time.Date(2026, 9, 1, 8, 0, 0, 0, loc).Unix()
	t2 := time.Date(2026, 9, 10, 12, 0, 0, 0, loc).Unix()
	t3 := time.Date(2026, 9, 20, 18, 0, 0, 0, loc).Unix()

	r.Observe(mac, ip1, t2)
	r.Observe(mac, ip1, t1) // 更早的观察应回填 first_seen
	r.Observe(mac, ip2, t3)
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SetName(ctx, mac, "  孩子的平板  "); err != nil {
		t.Fatal(err)
	}

	got := r.All()
	if len(got) != 1 {
		t.Fatalf("devices = %d", len(got))
	}
	d := got[0]
	if d.MAC != mac || d.Name != "孩子的平板" || d.FirstSeen != t1 || d.LastSeen != t3 {
		t.Fatalf("device = %+v", d)
	}
	if len(d.IPs) != 2 || d.IPs[0].IP != ip2 || d.IPs[1].IP != ip1 || d.IPs[1].FirstSeen != t1 {
		t.Fatalf("ips = %+v", d.IPs)
	}
	if r.LookupName(mac) != "孩子的平板" || r.Version() == 0 {
		t.Fatalf("name/version = %q %d", r.LookupName(mac), r.Version())
	}
}

func TestRegistryPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.db")
	mac := "aa:bb:cc:00:00:02"
	ip := netip.MustParseAddr("192.168.1.30")
	at := int64(1_700_000_000)

	r, err := OpenRegistry(ctx, RegistryOptions{Path: path, PersistEvery: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r.Observe(mac, ip, at)
	if err := r.SetName(ctx, mac, "电视"); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	r2, err := OpenRegistry(ctx, RegistryOptions{Path: path, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	all := r2.All()
	if len(all) != 1 || all[0].Name != "电视" || all[0].FirstSeen != at || len(all[0].IPs) != 1 {
		t.Fatalf("reopened = %+v", all)
	}
}

func TestRegistryConsumeAndIgnoreBadMAC(t *testing.T) {
	r := openTestRegistry(t, time.Nanosecond)
	err := r.Consume(context.Background(), model.UsageBatch{CollectedAt: 100, Events: []model.UsageEvent{
		{CollectedAt: 100, DeviceMAC: "aa:bb:cc:00:00:03", DeviceIP: netip.MustParseAddr("192.168.1.40")},
		{CollectedAt: 100, DeviceMAC: "", DeviceIP: netip.MustParseAddr("192.168.1.41")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.All()); n != 1 {
		t.Fatalf("devices = %d", n)
	}
}

func TestNormalizeDeviceName(t *testing.T) {
	if _, err := normalizeDeviceName(string(make([]rune, maxDeviceNameRunes+1))); err == nil {
		t.Fatal("expected too long")
	}
	s, err := normalizeDeviceName("  ok  ")
	if err != nil || s != "ok" || utf8.RuneCountInString(s) != 2 {
		t.Fatalf("got %q %v", s, err)
	}
}
