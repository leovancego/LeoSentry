package policy

import (
	"context"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/store"
)

func TestCollectMinutes(t *testing.T) {
	rules, err := category.Default()
	if err != nil {
		t.Fatal(err)
	}
	mac := "aa:bb:cc:00:00:01"
	rows := []store.UsageRow{
		{CollectedAt: 100, DeviceMAC: mac, Domain: "pvp.qq.com"},
		{CollectedAt: 100, DeviceMAC: mac, Domain: "pvp.qq.com"},
		{CollectedAt: 160, DeviceMAC: mac, Domain: "bilibili.com"},
		{CollectedAt: 220, DeviceMAC: "aa:bb:cc:00:00:02", Domain: "pvp.qq.com"},
	}
	scan := rowScan(rows)
	got, err := CollectMinutes(context.Background(), scan, rules, []string{mac}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got[mac].Internet != 2 || got[mac].Game != 1 {
		t.Fatalf("minutes = %+v", got[mac])
	}
	if _, ok := got["aa:bb:cc:00:00:02"]; ok {
		t.Fatal("unrequested device counted")
	}
	empty, err := CollectMinutes(context.Background(), scan, rules, nil, time.Minute)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %+v %v", empty, err)
	}
}

type rowScan []store.UsageRow

func (r rowScan) ScanDay(_ context.Context, _ func(time.Time), fn func(*store.UsageRow) error) error {
	for i := range r {
		if err := fn(&r[i]); err != nil {
			return err
		}
	}
	return nil
}
