package collector

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/collector/nftstats"
	"github.com/leo/leosentry/internal/model"
)

type fakeTraffic map[nftstats.FlowPair]nftstats.Traffic

func (f fakeTraffic) Read() (map[nftstats.FlowPair]nftstats.Traffic, error) { return f, nil }

type fakeDevices struct{ refreshed int }

func (f *fakeDevices) RefreshNeighbors() { f.refreshed++ }
func (f *fakeDevices) Lookup(ip netip.Addr) (model.Device, bool) {
	if ip.String() == "192.168.1.10" {
		return model.Device{MAC: "aa:bb:cc:00:00:01", IP: ip}, true
	}
	return model.Device{}, false
}

type fakeDomains map[string]string

func (f fakeDomains) Lookup(ip netip.Addr) string { return f[ip.String()] }

type captureSink struct{ batches []model.UsageBatch }

func (c *captureSink) Consume(_ context.Context, b model.UsageBatch) error {
	c.batches = append(c.batches, b)
	return nil
}

func TestCollect(t *testing.T) {
	sink := &captureSink{}
	devices := &fakeDevices{}
	c := New(Options{
		Interval: 10 * time.Second,
		Traffic: fakeTraffic{
			{Device: [4]byte{192, 168, 1, 10}, Target: [4]byte{1, 1, 1, 1}}: {BytesUp: 100, BytesDown: 2000},
			{Device: [4]byte{192, 168, 1, 99}, Target: [4]byte{2, 2, 2, 2}}: {BytesDown: 5},
		},
		Devices: devices,
		Domains: fakeDomains{"1.1.1.1": "game.com"},
		Sinks:   []Sink{sink},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	now := time.Unix(1_790_000_003, 0)
	c.collect(context.Background(), now)

	if len(sink.batches) != 1 || devices.refreshed != 1 {
		t.Fatalf("batches=%d refreshed=%d", len(sink.batches), devices.refreshed)
	}
	b := sink.batches[0]
	if b.CollectedAt != 1_790_000_000 || len(b.Events) != 2 {
		t.Fatalf("batch = %+v", b)
	}
	for _, ev := range b.Events {
		if ev.CollectedAt != b.CollectedAt {
			t.Error("events must share the batch timestamp")
		}
		switch ev.DeviceIP.String() {
		case "192.168.1.10":
			if ev.DeviceMAC != "aa:bb:cc:00:00:01" || ev.Domain != "game.com" || ev.BytesUp != 100 || ev.BytesDown != 2000 {
				t.Errorf("event = %+v", ev)
			}
		case "192.168.1.99":
			if ev.DeviceMAC != "" || ev.Domain != "" || ev.BytesDown != 5 {
				t.Errorf("event = %+v", ev)
			}
		}
	}
}

func TestRunFlushesPartialPeriodOnStop(t *testing.T) {
	sink := &captureSink{}
	c := New(Options{
		Interval: time.Hour,
		Traffic:  fakeTraffic{{Device: [4]byte{192, 168, 1, 10}, Target: [4]byte{1, 1, 1, 1}}: {BytesUp: 1}},
		Devices:  &fakeDevices{},
		Domains:  fakeDomains{},
		Sinks:    []Sink{sink},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := time.Now()
	c.Run(ctx)
	if len(sink.batches) != 1 {
		t.Fatalf("batches = %d, want final flush", len(sink.batches))
	}
	if want := before.Truncate(time.Hour).Add(time.Hour).Unix(); sink.batches[0].CollectedAt != want {
		t.Fatalf("collected_at = %d, want %d", sink.batches[0].CollectedAt, want)
	}
}

func TestCollectDropsSmallFlows(t *testing.T) {
	sink := &captureSink{}
	c := New(Options{
		Interval:     time.Minute,
		MinFlowBytes: 1024,
		Traffic: fakeTraffic{
			{Device: [4]byte{192, 168, 1, 10}, Target: [4]byte{1, 1, 1, 1}}: {BytesUp: 24, BytesDown: 1000},
			{Device: [4]byte{192, 168, 1, 10}, Target: [4]byte{2, 2, 2, 2}}: {BytesUp: 200, BytesDown: 300},
		},
		Devices: &fakeDevices{},
		Domains: fakeDomains{},
		Sinks:   []Sink{sink},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	c.collect(context.Background(), time.Unix(1_790_000_040, 0))
	if len(sink.batches) != 1 || len(sink.batches[0].Events) != 1 || sink.batches[0].Events[0].TargetIP.String() != "1.1.1.1" {
		t.Fatalf("batches = %+v", sink.batches)
	}

	// 全部低于阈值时不产生批次
	c.opts.Traffic = fakeTraffic{{Device: [4]byte{192, 168, 1, 10}, Target: [4]byte{2, 2, 2, 2}}: {BytesUp: 10}}
	c.collect(context.Background(), time.Unix(1_790_000_100, 0))
	if len(sink.batches) != 1 {
		t.Fatalf("empty batch dispatched: %d", len(sink.batches))
	}
}
