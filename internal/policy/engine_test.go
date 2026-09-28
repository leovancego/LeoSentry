package policy

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/nftctl"
	"github.com/leo/leosentry/internal/statday"
	"github.com/leo/leosentry/internal/store"
)

func TestEngineAppliesOnlyWhenControlChanges(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenPolicy(ctx, store.PolicyPath(t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Seed(ctx, db); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)
	clk := time.Date(2026, 9, 26, 15, 0, 0, 0, loc)
	mac := "aa:bb:cc:00:00:01"
	ip := netip.MustParseAddr("192.168.1.10")
	fw := &memFW{}
	eng := New(Options{
		DB:        db,
		Usage:     fixedUsage{mac: Minutes{Internet: 40, Game: 30}},
		Addresses: addrBook{{MAC: mac, IP: ip}},
		Stat:      statday.New(3, 0, loc),
		Firewall:  fw,
		Now:       func() time.Time { return clk },
	})

	// 中秋是法定休息日。只设工作日上限时，不应拦截。
	if _, err := eng.Save(ctx, mac, Save{
		Enabled: true,
		DailyLimits: []DailyLimit{{
			Days: DaySelector{Kind: DayWorkday}, GameMinutes: 10, InternetMinutes: 10,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := eng.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ByMAC[mac].Action != ActionAllow || snap.Calendar.Today.Name != "中秋节" || snap.Calendar.Today.Workday {
		t.Fatalf("mid-autumn = %+v", snap.ByMAC[mac])
	}
	if fw.writes != 0 {
		t.Fatalf("allow should not write nftables, writes = %d", fw.writes)
	}

	if _, err := eng.Save(ctx, mac, Save{
		Enabled: true,
		DailyLimits: []DailyLimit{{
			Days: DaySelector{Kind: DayRestday}, GameMinutes: 30, InternetMinutes: 180,
		}},
		Windows: []Window{{
			Days: DaySelector{Kind: DayWorkday}, Start: "21:30", End: "07:00", BlockGame: true, BlockVideo: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if snap, err = eng.Current(ctx); err != nil {
		t.Fatal(err)
	}
	dec := snap.ByMAC[mac]
	if dec.Action != ActionBlockGame || dec.Reason != ReasonGame || dec.LimitLabel != "法定休息日" {
		t.Fatalf("decision = %+v", dec)
	}
	if fw.writes != 1 || !slices.Contains(fw.last.BlockGame, ip) || len(fw.last.BlockAll) != 0 {
		t.Fatalf("targets = %+v writes %d", fw.last, fw.writes)
	}
	if _, err := eng.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if fw.writes != 1 || fw.calls < 2 {
		t.Fatalf("unchanged check wrote nftables: calls %d writes %d", fw.calls, fw.writes)
	}

	if _, err := eng.Pause(ctx, mac, true); err != nil {
		t.Fatal(err)
	}
	if fw.writes != 2 || !slices.Contains(fw.last.BlockAll, ip) || !slices.Contains(fw.last.BlockAllMAC, mac) || len(fw.last.BlockGame) != 0 {
		t.Fatalf("pause targets = %+v", fw.last)
	}
	// 保存规则不应清掉暂停。
	if _, err := eng.Save(ctx, mac, Save{
		Enabled: true,
		DailyLimits: []DailyLimit{{
			Days: DaySelector{Kind: DayRestday}, GameMinutes: 30, InternetMinutes: 180,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if snap, _ = eng.Current(ctx); !snap.ByMAC[mac].Paused || snap.ByMAC[mac].Action != ActionBlockAll {
		t.Fatalf("pause lost: %+v", snap.ByMAC[mac])
	}
	if _, err := eng.Pause(ctx, mac, false); err != nil {
		t.Fatal(err)
	}
	if snap, _ = eng.Current(ctx); snap.ByMAC[mac].Action != ActionBlockGame {
		t.Fatalf("resume = %+v", snap.ByMAC[mac])
	}

	if _, err := eng.Extend(ctx, mac, 30); err != nil {
		t.Fatal(err)
	}
	if snap, _ = eng.Current(ctx); snap.ByMAC[mac].Reason != ReasonExtend || len(fw.last.BlockGame) != 0 || len(fw.last.BlockAll) != 0 {
		t.Fatalf("extend = %+v targets %+v", snap.ByMAC[mac], fw.last)
	}
	clk = clk.Add(31 * time.Minute)
	if _, err := eng.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if snap, _ = eng.Current(ctx); snap.ByMAC[mac].Reason != ReasonGame {
		t.Fatalf("after extend = %+v", snap.ByMAC[mac])
	}

	if _, err := eng.Extend(ctx, mac, 15); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Pause(ctx, mac, true); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Extend(ctx, mac, 15); err == nil {
		t.Fatal("extend during pause should fail")
	}
}

type memFW struct {
	last          nftctl.Targets
	calls, writes int
}

func (m *memFW) Apply(t nftctl.Targets) (bool, error) {
	m.calls++
	_, changed := nftctl.Diff(m.last, t)
	if changed {
		m.writes++
		m.last = nftctl.Canon(t)
	}
	return changed, nil
}

type addrBook []model.Device

func (a addrBook) Devices() []model.Device { return a }

type fixedUsage map[string]Minutes

func (f fixedUsage) Minutes(context.Context, []string) (map[string]Minutes, error) { return f, nil }
