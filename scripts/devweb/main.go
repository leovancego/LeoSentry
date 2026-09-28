// 本机预览设备管理页：不依赖 nft/conntrack，用假数据提供 API。
//
//	go run ./scripts/devweb
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"time"

	"github.com/leo/leosentry/internal/analyzer/activity"
	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/api"
	"github.com/leo/leosentry/internal/device"
	"github.com/leo/leosentry/internal/installer"
	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/policy"
	"github.com/leo/leosentry/internal/settings"
	"github.com/leo/leosentry/internal/statday"
	"github.com/leo/leosentry/internal/store"
)

type stubOverview struct{ snap *activity.Snapshot }

func (s stubOverview) Snapshot(context.Context, string) (*activity.Snapshot, error) {
	return s.snap, nil
}

type live []model.Device

func (l live) Devices() []model.Device { return l }

type online []model.DeviceActivity

func (o online) Activities() []model.DeviceActivity { return o }

type demoUsage map[string]policy.Minutes

func (d demoUsage) Minutes(context.Context, []string) (map[string]policy.Minutes, error) {
	return d, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	dir, err := os.MkdirTemp("", "leosentry-devweb-")
	if err != nil {
		panic(err)
	}
	reg, err := store.OpenRegistry(ctx, store.RegistryOptions{Path: store.DevicesPath(dir), Logger: log})
	if err != nil {
		panic(err)
	}
	defer reg.Close()

	loc := time.FixedZone("CST", 8*3600)
	t1 := time.Date(2026, 8, 1, 9, 10, 0, 0, loc).Unix()
	t2 := time.Date(2026, 9, 10, 18, 20, 0, 0, loc).Unix()
	t3 := time.Date(2026, 9, 26, 16, 0, 0, 0, loc).Unix()
	pad := netip.MustParseAddr("192.168.1.10")
	old := netip.MustParseAddr("192.168.1.20")
	tv := netip.MustParseAddr("192.168.1.30")
	reg.Observe("aa:bb:cc:00:00:01", old, t1)
	reg.Observe("aa:bb:cc:00:00:01", pad, t3)
	reg.Observe("aa:bb:cc:00:00:02", tv, t2)
	_ = reg.SetName(ctx, "aa:bb:cc:00:00:02", "客厅电视")
	_ = reg.Flush(ctx)

	cat := device.NewCatalog(device.CatalogOptions{
		Book:     reg,
		Location: loc,
		Now:      func() time.Time { return time.Date(2026, 9, 26, 17, 22, 0, 0, loc) },
		Live: live{
			{MAC: "aa:bb:cc:00:00:01", IP: pad, Hostname: "kid-ipad", Source: model.SourceLease},
			{MAC: "aa:bb:cc:00:00:02", IP: tv, Hostname: "smart-tv", Source: model.SourceStatic},
			{MAC: "aa:bb:cc:00:00:03", IP: netip.MustParseAddr("192.168.1.40"), Hostname: "nas", Source: model.SourceNeighbor},
		},
		Online: online{
			{MAC: "aa:bb:cc:00:00:01", IP: pad, Hostname: "kid-ipad", Online: true, Flows: 5, LastActiveAt: time.Unix(t3, 0)},
		},
	})

	pdb, err := store.OpenPolicy(ctx, store.PolicyPath(dir), log)
	if err != nil {
		panic(err)
	}
	defer pdb.Close()
	if err := policy.Seed(ctx, pdb); err != nil {
		panic(err)
	}
	matcher, err := settings.LoadRules(ctx, pdb)
	if err != nil {
		panic(err)
	}
	rulesLive := category.NewLive(matcher)
	eng := policy.New(policy.Options{
		DB:    pdb,
		Stat:  statday.New(3, 0, loc),
		Usage: demoUsage{"aa:bb:cc:00:00:01": {Internet: 80, Game: 45}},
		Addresses: live{
			{MAC: "aa:bb:cc:00:00:01", IP: pad, Hostname: "kid-ipad", Source: model.SourceLease},
			{MAC: "aa:bb:cc:00:00:02", IP: tv, Hostname: "smart-tv", Source: model.SourceStatic},
			{MAC: "aa:bb:cc:00:00:03", IP: netip.MustParseAddr("192.168.1.40"), Hostname: "nas", Source: model.SourceNeighbor},
		},
		Live:   rulesLive,
		Logger: log,
		Now:    func() time.Time { return time.Date(2026, 9, 26, 17, 22, 0, 0, loc) },
	})
	if _, err := eng.Save(ctx, "aa:bb:cc:00:00:01", policy.Save{
		Enabled: true,
		DailyLimits: []policy.DailyLimit{
			{Days: policy.DaySelector{Kind: policy.DayWorkday}, GameMinutes: 60, InternetMinutes: 180},
			{Days: policy.DaySelector{Kind: policy.DayRestday}, GameMinutes: 120, InternetMinutes: 300},
		},
		Windows: []policy.Window{{
			Days: policy.DaySelector{Kind: policy.DayWorkday}, Start: "21:00", End: "07:00",
			BlockGame: true, BlockVideo: true,
		}},
	}); err != nil {
		panic(err)
	}

	raw := []byte(`{"generatedAt":1,"day":"2026-09-26","categories":[],"apps":[],"devices":[],"deviceBuckets":[],"categoryBuckets":[],"entBuckets":[],"appBuckets":[],"recent":[],"stats":{}}`)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()

	pref := settings.Bind(pdb, rulesLive, nil, nil, eng, "03:00")
	pref.UseTLS("", "", 8088, 8443)
	srv, err := api.New(api.Options{
		Addrs:     []string{"127.0.0.1:18088"},
		Overview:  stubOverview{&activity.Snapshot{ETag: `"dev"`, JSON: raw, Gzip: buf.Bytes()}},
		Devices:   cat,
		Policies:  eng,
		Settings:  pref,
		Passwords: pdb,
		Restart:   func() error { return installer.Restart(log) },
		Logger:    log,
	})
	if err != nil {
		panic(err)
	}
	if err := srv.Run(ctx); err != nil {
		log.Error("server", "err", err)
	}
}
