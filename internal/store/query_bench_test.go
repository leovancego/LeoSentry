package store

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/model"
)

// BenchmarkScanDay 模拟一整天的明细（1440 个周期 × 26 行）后扫描一遍。
func BenchmarkScanDay(b *testing.B) {
	c := &clock{t: time.Date(2026, 9, 26, 3, 0, 0, 0, loc)}
	s := newTestStore(b, b.TempDir(), "", c)
	defer s.Close()
	ctx := context.Background()
	start := c.now()
	for p := range 1440 {
		at := start.Add(time.Duration(p+1) * time.Minute).Unix()
		batch := model.UsageBatch{CollectedAt: at}
		for i := range 26 {
			target := (p*7 + i*13) % 400
			domain := ""
			if (p+i)%3 != 0 {
				domain = fmt.Sprintf("site%d.example.com", target)
			}
			batch.Events = append(batch.Events, model.UsageEvent{
				CollectedAt: at,
				DeviceMAC:   fmt.Sprintf("aa:bb:cc:00:00:%02x", i%10),
				DeviceIP:    netip.AddrFrom4([4]byte{192, 168, 1, byte(10 + i%10)}),
				TargetIP:    netip.AddrFrom4([4]byte{10, 0, byte(target >> 8), byte(target)}),
				Domain:      domain,
				BytesUp:     1000,
				BytesDown:   50000,
			})
		}
		if err := s.Consume(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		n := 0
		if err := s.ScanDay(ctx, func(time.Time) {}, func(*UsageRow) error { n++; return nil }); err != nil {
			b.Fatal(err)
		}
		if n != 1440*26 {
			b.Fatalf("rows = %d", n)
		}
	}
}
