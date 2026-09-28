package activity

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/statday"
	"github.com/leo/leosentry/internal/store"
)

var loc = time.FixedZone("CST", 8*3600)

type fakeUsage struct {
	dayStart time.Time
	rows     []store.UsageRow
	version  uint64
	scans    int
}

func (f *fakeUsage) ScanDay(_ context.Context, begin func(time.Time), fn func(*store.UsageRow) error) error {
	f.scans++
	begin(f.dayStart)
	for i := range f.rows {
		if err := fn(&f.rows[i]); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeUsage) Version() uint64 { return f.version }

type fakeDevices []model.Device

func (f fakeDevices) Devices() []model.Device { return f }

type fakeOnline []model.DeviceActivity

func (f fakeOnline) Activities() []model.DeviceActivity { return f }

const (
	kid   = "aa:bb:cc:00:00:01"
	tv    = "aa:bb:cc:00:00:02"
	kidIP = "192.168.1.10"
)

func row(at time.Time, mac, ip, target, domain string, up, down int64) store.UsageRow {
	return store.UsageRow{
		CollectedAt: at.Unix(), DeviceMAC: mac,
		DeviceIP: netip.MustParseAddr(ip), TargetIP: netip.MustParseAddr(target),
		Domain: domain, BytesUp: up, BytesDown: down,
	}
}

func newTestBuilder(t *testing.T, usage *fakeUsage, now *time.Time) *Builder {
	t.Helper()
	rules, err := category.Default()
	if err != nil {
		t.Fatal(err)
	}
	return NewBuilder(Options{
		Usage:    usage,
		Devices:  fakeDevices{{MAC: kid, IP: netip.MustParseAddr(kidIP), Hostname: "kid-pad"}},
		Online:   fakeOnline{{MAC: "aa:bb:cc:00:00:03", IP: netip.MustParseAddr("192.168.1.30"), Online: true, Flows: 4}},
		Rules:    rules,
		Calendar: statday.New(3, 0, loc),
		Interval: time.Minute,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:      func() time.Time { return *now },
	})
}

func TestBuild(t *testing.T) {
	day := time.Date(2026, 9, 26, 3, 0, 0, 0, loc)
	m := func(h, min int) time.Time { return time.Date(2026, 9, 26, h, min, 0, 0, loc) }
	usage := &fakeUsage{dayStart: day, version: 1, rows: []store.UsageRow{
		// 20:01 这一分钟：游戏 + 未识别，同一分钟内设备只计 1 个周期
		row(m(20, 1), kid, kidIP, "1.1.1.1", "ds-prod-gz-20.df.qq.com", 100, 900),
		row(m(20, 1), kid, kidIP, "2.2.2.2", "", 10, 10),
		row(m(20, 2), kid, kidIP, "1.1.1.1", "ds-prod-gz-20.df.qq.com", 100, 900),
		row(m(20, 2), kid, kidIP, "3.3.3.3", "xp.apple.com", 5, 5),
		// 20:11 进入下一个 10 分钟时段
		row(m(20, 11), kid, kidIP, "1.1.1.1", "ds-prod-gz-20.df.qq.com", 100, 900),
		row(m(20, 11), tv, "192.168.1.20", "4.4.4.4", "grpc.snm0516.aisee.tv", 1, 5000),
		row(m(20, 11), tv, "192.168.1.20", "5.5.5.5", "cdn.somesite.com.cn", 1, 1),
	}}
	now := m(20, 12)
	b := newTestBuilder(t, usage, &now)
	ov, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if ov.Buckets != 144 || ov.DayStart != day.Unix() || ov.TZOffset != 8*3600 || ov.LastCollectedAt != m(20, 11).Unix() {
		t.Fatalf("header = %+v", ov)
	}
	if len(ov.Devices) != 3 || ov.Devices[0].Hostname != "kid-pad" || ov.Devices[2].Online != true || ov.Devices[2].Flows != 4 {
		t.Fatalf("devices = %+v", ov.Devices)
	}
	if h := ov.Devices[0].Hints; len(h) != 1 || h[0] != "苹果设备" {
		t.Errorf("hints = %v", h)
	}

	bucket2000 := int64((m(20, 0).Unix() - day.Unix()) / 600)
	wantDev := [][5]int64{
		{0, bucket2000, 2, 215, 1815},
		{0, bucket2000 + 1, 1, 100, 900},
		{1, bucket2000 + 1, 1, 2, 5001},
	}
	if len(ov.DeviceBuckets) != len(wantDev) {
		t.Fatalf("device buckets = %v", ov.DeviceBuckets)
	}
	for i, w := range wantDev {
		if ov.DeviceBuckets[i] != w {
			t.Errorf("device bucket %d = %v, want %v", i, ov.DeviceBuckets[i], w)
		}
	}

	appName := func(i int64) string { return ov.Apps[i].Name }
	catID := func(i int64) string { return ov.Categories[i].ID }
	var gamePeriods, entKid int64
	for _, r := range ov.AppBuckets {
		if r[0] == 0 && appName(r[2]) == "三角洲行动" {
			gamePeriods += r[3]
		}
	}
	for _, r := range ov.EntBuckets {
		if r[0] == 0 {
			entKid += r[2]
		}
	}
	if gamePeriods != 3 || entKid != 3 {
		t.Errorf("game periods = %d, ent = %d", gamePeriods, entKid)
	}
	cats := map[string]int64{}
	for _, r := range ov.CategoryBuckets {
		cats[catID(r[2])] += r[3]
	}
	if cats["game"] != 3 || cats["unknown"] != 1 || cats["system"] != 1 || cats["video"] != 1 || cats["other"] != 1 {
		t.Errorf("category periods = %v", cats)
	}
	var site bool
	for _, a := range ov.Apps {
		if a.Name == "somesite.com.cn" && !a.Rule && ov.Categories[a.Category].ID == "other" {
			site = true
		}
	}
	if !site {
		t.Errorf("unmatched domain not grouped by site: %+v", ov.Apps)
	}
	// 最近 5 分钟（20:07 之后）只有 20:11 的记录
	if len(ov.Recent) != 3 {
		t.Errorf("recent = %v", ov.Recent)
	}
	if ov.Stats.Rows != 7 || ov.Stats.UnknownRows != 1 {
		t.Errorf("stats = %+v", ov.Stats)
	}
}

type fakeNames struct {
	names map[string]string
	ver   uint64
}

func (f *fakeNames) LookupName(mac string) string { return f.names[mac] }
func (f *fakeNames) Version() uint64              { return f.ver }

func TestBuildUsesDeviceName(t *testing.T) {
	day := time.Date(2026, 9, 26, 3, 0, 0, 0, loc)
	now := day.Add(time.Hour)
	usage := &fakeUsage{dayStart: day, version: 1, rows: []store.UsageRow{
		row(now, kid, kidIP, "1.1.1.1", "", 1, 1),
	}}
	b := newTestBuilder(t, usage, &now)
	b.opts.Names = &fakeNames{names: map[string]string{kid: "孩子的平板"}}
	ov, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ov.Devices[0].Name != "孩子的平板" {
		t.Fatalf("name = %q", ov.Devices[0].Name)
	}

	s1, _ := b.Snapshot(context.Background(), "today")
	b.opts.Names.(*fakeNames).ver = 2
	b.opts.Names.(*fakeNames).names[kid] = "改名了"
	s2, _ := b.Snapshot(context.Background(), "today")
	if s1 == s2 {
		t.Fatal("name version change must rebuild")
	}
}

func TestSnapshotCache(t *testing.T) {
	day := time.Date(2026, 9, 26, 3, 0, 0, 0, loc)
	usage := &fakeUsage{dayStart: day, version: 1}
	now := day.Add(time.Hour)
	b := newTestBuilder(t, usage, &now)
	ctx := context.Background()

	s1, err := b.Snapshot(ctx, "today")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := b.Snapshot(ctx, "today")
	if s1 != s2 || usage.scans != 1 {
		t.Fatalf("expected cached snapshot, scans = %d", usage.scans)
	}
	usage.version = 2
	s3, _ := b.Snapshot(ctx, "today")
	if s3 == s1 || usage.scans != 2 || s3.ETag == s1.ETag {
		t.Fatalf("version change must rebuild, scans = %d", usage.scans)
	}
	now = now.Add(maxCacheAge)
	if s4, _ := b.Snapshot(ctx, "today"); s4 == s3 {
		t.Fatal("expired cache must rebuild")
	}

	zr, err := gzip.NewReader(bytes.NewReader(s3.Gzip))
	if err != nil {
		t.Fatal(err)
	}
	var ov Overview
	if err := json.NewDecoder(zr).Decode(&ov); err != nil || ov.Buckets != 144 {
		t.Fatalf("gzip payload: %v %+v", err, ov)
	}
}
