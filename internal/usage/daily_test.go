package usage

import (
	"context"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/statday"
)

func TestDailyConsumeAndRollover(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cal := statday.New(3, 0, loc)
	d := NewDaily(cal, 10*time.Second)

	day1 := time.Date(2026, 9, 26, 20, 0, 0, 0, loc)
	d.dayStart = cal.DayStart(day1)
	batch := func(t time.Time, evs ...model.UsageEvent) model.UsageBatch {
		for i := range evs {
			evs[i].CollectedAt = t.Unix()
		}
		return model.UsageBatch{CollectedAt: t.Unix(), Events: evs}
	}
	mac := "aa:bb:cc:00:00:01"
	ctx := context.Background()
	_ = d.Consume(ctx, batch(day1,
		model.UsageEvent{DeviceMAC: mac, Domain: "game.com", BytesUp: 10, BytesDown: 100},
		model.UsageEvent{DeviceMAC: mac, Domain: "game.com", BytesUp: 1, BytesDown: 1},
		model.UsageEvent{DeviceMAC: mac, Domain: "", BytesDown: 5},
	))
	_ = d.Consume(ctx, batch(day1.Add(10*time.Second),
		model.UsageEvent{DeviceMAC: mac, Domain: "game.com", BytesDown: 50},
	))

	dt, ok := d.Device(mac)
	if !ok {
		t.Fatal("device missing")
	}
	if dt.BytesUp != 11 || dt.BytesDown != 156 || dt.ActiveSeconds != 20 {
		t.Errorf("device totals = %+v", dt.Totals)
	}
	if g := dt.Domains["game.com"]; g.ActiveSeconds != 20 || g.BytesDown != 151 {
		t.Errorf("game.com = %+v", g)
	}
	if u := dt.Domains[""]; u.ActiveSeconds != 10 {
		t.Errorf("unknown domain = %+v", u)
	}

	// 次日 03:00 之后的批次触发清零
	_ = d.Consume(ctx, batch(time.Date(2026, 9, 27, 3, 0, 10, 0, loc),
		model.UsageEvent{DeviceMAC: mac, BytesDown: 7},
	))
	dt, _ = d.Device(mac)
	if dt.BytesDown != 7 || dt.ActiveSeconds != 10 {
		t.Errorf("after rollover = %+v", dt.Totals)
	}
}

func TestDailyRestore(t *testing.T) {
	cal := statday.New(3, 0, time.UTC)
	d := NewDaily(cal, 10*time.Second)
	d.Restore(cal.DayStart(time.Now()),
		[]model.UsageTotal{{DeviceMAC: "m", BytesUp: 1, BytesDown: 2, ActivePeriods: 6}},
		[]model.UsageTotal{{DeviceMAC: "m", Domain: "a.com", BytesDown: 2, ActivePeriods: 3}},
	)
	dt, _ := d.Device("m")
	if dt.ActiveSeconds != 60 || dt.Domains["a.com"].ActiveSeconds != 30 {
		t.Fatalf("restored = %+v", dt)
	}
}
