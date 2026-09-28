package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/statday"
)

var loc = time.FixedZone("CST", 8*3600)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func newTestStore(t testing.TB, dataDir, archiveDir string, c *clock) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{
		DataDir:         dataDir,
		ArchiveDir:      archiveDir,
		ArchiveKeepDays: 30,
		Calendar:        statday.New(3, 0, loc),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:             c.now,
		isExternal:      func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func event(ts time.Time, mac, target, domain string, up, down int64) model.UsageEvent {
	return model.UsageEvent{
		CollectedAt: ts.Unix(),
		DeviceMAC:   mac,
		DeviceIP:    netip.MustParseAddr("192.168.1.10"),
		TargetIP:    netip.MustParseAddr(target),
		Domain:      domain,
		BytesUp:     up,
		BytesDown:   down,
	}
}

func batchOf(evs ...model.UsageEvent) model.UsageBatch {
	return model.UsageBatch{CollectedAt: evs[0].CollectedAt, Events: evs}
}

func TestOpenInitializesSchemaOnce(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 9, 26, 14, 0, 0, 0, loc)}
	s := newTestStore(t, dir, "", c)
	if got := s.DayStart(); !got.Equal(time.Date(2026, 9, 26, 3, 0, 0, 0, loc)) {
		t.Fatalf("day start = %s", got)
	}
	s.Close()

	// 再次打开：版本号已是最新，不应重复执行迁移
	s = newTestStore(t, dir, "", c)
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion() {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion())
	}
	var journal string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal_mode = %q, %v", journal, err)
	}
}

func TestConsumeUpsertAndTotals(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 26, 14, 0, 0, 0, loc)}
	s := newTestStore(t, t.TempDir(), "", c)
	defer s.Close()
	ctx := context.Background()

	t0 := c.now()
	mac := "aa:bb:cc:00:00:01"
	must(t, s.Consume(ctx, batchOf(
		event(t0, mac, "1.1.1.1", "", 10, 100),
		event(t0, mac, "2.2.2.2", "video.com", 1, 1000),
	)))
	// 同一周期重放：字节累加、域名补填
	must(t, s.Consume(ctx, batchOf(event(t0, mac, "1.1.1.1", "game.com", 5, 50))))
	must(t, s.Consume(ctx, batchOf(event(t0.Add(10*time.Second), mac, "1.1.1.1", "", 1, 1))))

	var up, down int64
	var domain sql.NullString
	err := s.db.QueryRow(`SELECT bytes_up, bytes_down, domain FROM usage_records
		WHERE collected_at = ? AND device_mac = ? AND target_ip = ?`,
		t0.Unix(), encodeMAC(mac), encodeIPv4(netip.MustParseAddr("1.1.1.1"))).Scan(&up, &down, &domain)
	must(t, err)
	if up != 15 || down != 150 || domain.String != "game.com" {
		t.Fatalf("merged row = %d %d %v", up, down, domain)
	}

	var textMAC, textDevice, textTarget string
	must(t, s.db.QueryRow(`SELECT device_mac, device_ip, target_ip FROM v_usage_records
		WHERE domain = 'video.com'`).Scan(&textMAC, &textDevice, &textTarget))
	if textMAC != mac || textDevice != "192.168.1.10" || textTarget != "2.2.2.2" {
		t.Fatalf("readable view = %s %s %s", textMAC, textDevice, textTarget)
	}

	_, devices, domains, err := s.DailyTotals(ctx)
	must(t, err)
	if len(devices) != 1 || devices[0].DeviceMAC != mac || devices[0].BytesDown != 1151 || devices[0].ActivePeriods != 2 {
		t.Fatalf("devices = %+v", devices)
	}
	byDomain := map[string]model.UsageTotal{}
	for _, d := range domains {
		byDomain[d.Domain] = d
	}
	if byDomain["game.com"].ActivePeriods != 1 || byDomain[""].ActivePeriods != 1 || byDomain["video.com"].BytesDown != 1000 {
		t.Fatalf("domains = %+v", domains)
	}

	seen, err := s.DeviceSightings(ctx)
	must(t, err)
	if len(seen) != 1 || seen[0].MAC != mac || seen[0].IP.String() != "192.168.1.10" || seen[0].First != t0.Unix() {
		t.Fatalf("sightings = %+v", seen)
	}
}

func TestKeepsYesterdayAndDropsTheDayBefore(t *testing.T) {
	dataDir := t.TempDir()
	c := &clock{t: time.Date(2026, 9, 26, 23, 0, 0, 0, loc)}
	s := newTestStore(t, dataDir, "", c)
	defer s.Close()
	ctx := context.Background()
	mac := "aa:bb:cc:00:00:01"
	must(t, s.Consume(ctx, batchOf(event(c.now(), mac, "1.1.1.1", "a.com", 1, 2))))

	day27 := time.Date(2026, 9, 27, 3, 0, 10, 0, loc)
	c.set(day27)
	must(t, s.Consume(ctx, batchOf(event(day27, mac, "1.1.1.1", "a.com", 3, 4))))
	if s.DayStart().Day() != 27 {
		t.Fatalf("today = %s", s.DayStart())
	}
	var todayN, yestN int
	must(t, s.ScanDay(ctx, func(time.Time) {}, func(*UsageRow) error { todayN++; return nil }))
	must(t, s.ScanYesterday(ctx, func(time.Time) {}, func(*UsageRow) error { yestN++; return nil }))
	if todayN != 1 || yestN != 1 {
		t.Fatalf("today=%d yesterday=%d", todayN, yestN)
	}

	day28 := time.Date(2026, 9, 28, 3, 0, 10, 0, loc)
	c.set(day28)
	must(t, s.Consume(ctx, batchOf(event(day28, mac, "1.1.1.1", "b.com", 5, 6))))
	todayN, yestN = 0, 0
	var yestDomain string
	must(t, s.ScanDay(ctx, func(time.Time) {}, func(*UsageRow) error { todayN++; return nil }))
	must(t, s.ScanYesterday(ctx, func(time.Time) {}, func(r *UsageRow) error {
		yestN++
		yestDomain = r.Domain
		return nil
	}))
	if todayN != 1 || yestN != 1 || yestDomain != "a.com" || s.DayStart().Day() != 28 {
		t.Fatalf("today=%d yesterday=%d domain=%s day=%s", todayN, yestN, yestDomain, s.DayStart())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "prev.db")); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRotatesStaleDatabase(t *testing.T) {
	dataDir := t.TempDir()
	c := &clock{t: time.Date(2026, 9, 26, 14, 0, 0, 0, loc)}
	s := newTestStore(t, dataDir, "", c)
	must(t, s.Consume(context.Background(), batchOf(event(c.now(), "aa:bb:cc:00:00:01", "1.1.1.1", "", 1, 1))))
	s.Close()

	// 程序停机跨过了切换时刻
	c.set(time.Date(2026, 9, 28, 10, 0, 0, 0, loc))
	s = newTestStore(t, dataDir, "", c)
	defer s.Close()
	if s.DayStart().Day() != 28 {
		t.Fatalf("day start = %s", s.DayStart())
	}
	var n int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&n))
	if n != 0 {
		t.Fatalf("stale rows carried over: %d", n)
	}
}

func TestArchiveCleanupAndUnconfigured(t *testing.T) {
	dataDir, archiveDir := t.TempDir(), t.TempDir()
	old := filepath.Join(archiveDir, "archive_2026-07-01.db")
	recent := filepath.Join(archiveDir, "archive_2026-09-20.db")
	for _, f := range []string{old, recent} {
		must(t, os.WriteFile(f, []byte("x"), 0o644))
	}
	c := &clock{t: time.Date(2026, 9, 26, 14, 0, 0, 0, loc)}
	s := newTestStore(t, dataDir, archiveDir, c)
	s.archiver.process()
	s.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("expired archive not removed")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("recent archive removed")
	}
}

func TestScanDayInfersMissingDomain(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 26, 14, 0, 0, 0, loc)}
	s := newTestStore(t, t.TempDir(), "", c)
	defer s.Close()
	ctx := context.Background()
	mac := "aa:bb:cc:00:00:01"
	t0 := c.now()

	v0 := s.Version()
	must(t, s.Consume(ctx, batchOf(event(t0, mac, "1.1.1.1", "game.com", 1, 2))))
	must(t, s.Consume(ctx, batchOf(
		event(t0.Add(time.Minute), mac, "1.1.1.1", "", 3, 4),
		event(t0.Add(time.Minute), mac, "2.2.2.2", "", 5, 6),
	)))
	if s.Version() != v0+2 {
		t.Fatalf("version = %d, want %d", s.Version(), v0+2)
	}

	var got []UsageRow
	var day time.Time
	must(t, s.ScanDay(ctx, func(d time.Time) { day = d }, func(r *UsageRow) error { got = append(got, *r); return nil }))
	if !day.Equal(s.DayStart()) || len(got) != 3 {
		t.Fatalf("day=%s rows=%d", day, len(got))
	}
	if got[0].Domain != "game.com" || got[0].DomainInferred || got[0].DeviceMAC != mac {
		t.Errorf("row0 = %+v", got[0])
	}
	for _, r := range got[1:] {
		switch r.TargetIP.String() {
		case "1.1.1.1":
			if r.Domain != "game.com" || !r.DomainInferred || r.BytesDown != 4 {
				t.Errorf("inferred row = %+v", r)
			}
		case "2.2.2.2":
			if r.Domain != "" || r.DomainInferred {
				t.Errorf("unknown row = %+v", r)
			}
		}
	}
}

func TestConsumePausedWhenDiskLow(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 26, 14, 0, 0, 0, loc)}
	var free uint64 = 10 << 20
	s, err := Open(context.Background(), Options{
		DataDir:         t.TempDir(),
		ArchiveKeepDays: 30,
		Calendar:        statday.New(3, 0, loc),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		MinFreeBytes:    20 << 20,
		now:             c.now,
		isExternal:      func(string) bool { return true },
		freeSpace:       func(string) (uint64, error) { return free, nil },
	})
	must(t, err)
	defer s.Close()
	ctx := context.Background()
	mac := "aa:bb:cc:00:00:01"

	must(t, s.Consume(ctx, batchOf(event(c.now(), mac, "1.1.1.1", "", 1, 1))))
	free = 30 << 20
	must(t, s.Consume(ctx, batchOf(event(c.now().Add(time.Minute), mac, "1.1.1.1", "", 1, 1))))

	var n int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&n))
	if n != 1 {
		t.Fatalf("rows = %d, want only the batch written after space recovered", n)
	}
}

func TestCodec(t *testing.T) {
	for _, mac := range []string{"aa:bb:cc:00:00:01", "ff:ff:ff:ff:ff:ff", "00:00:00:00:00:01"} {
		if got := decodeMAC(encodeMAC(mac)); got != mac {
			t.Errorf("mac round trip %s = %s", mac, got)
		}
	}
	if encodeMAC("") != 0 || decodeMAC(0) != "" || encodeMAC("bogus") != 0 {
		t.Error("unknown mac must map to 0 and back to empty")
	}
	ip := netip.MustParseAddr("192.168.1.10")
	if v := encodeIPv4(ip); v != 0xC0A8010A || decodeIPv4(v) != ip {
		t.Errorf("ipv4 encode = %#x", v)
	}
	if encodeIPv4(netip.MustParseAddr("::1")) != 0 {
		t.Error("ipv6 must map to 0")
	}
}

func TestOnExternalStorage(t *testing.T) {
	mounts := []byte(`/dev/root /rom squashfs ro 0 0
overlayfs:/overlay / overlay rw 0 0
tmpfs /tmp tmpfs rw 0 0
/dev/sda1 /mnt/sda1 ext4 rw 0 0
/dev/sdb1 /mnt/my\040disk ext4 rw 0 0
`)
	cases := map[string]bool{
		"/mnt/sda1/leosentry_archive": true,
		"/mnt/my disk/archive":        true,
		"/mnt/sdc1/leosentry_archive": false,
		"/tmp/archive":                false,
		"/etc/leosentry":              false,
	}
	for dir, want := range cases {
		if got := onExternalStorageIn(mounts, dir); got != want {
			t.Errorf("%s = %v, want %v", dir, got, want)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
