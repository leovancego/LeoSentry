// Package usage 维护内存中的当日累计计数器，供 Web 面板高频查询"今天用了多久"，
// 不查询 today.db。程序重启时从 today.db 汇总恢复。
package usage

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/statday"
)

// Totals 是一组累计值。
type Totals struct {
	BytesUp   int64
	BytesDown int64
	// ActiveSeconds = 有流量的采集周期数 × 采集周期。
	ActiveSeconds int64
}

// DeviceTotals 是一台设备在统计日内的累计值，Domains 以域名为键（未知域名为空串）。
type DeviceTotals struct {
	Totals
	Domains map[string]Totals
}

// Daily 是当日累计计数器。
type Daily struct {
	cal      statday.Calendar
	interval int64

	mu       sync.RWMutex
	dayStart time.Time
	devices  map[string]*DeviceTotals
}

// NewDaily 创建计数器。
func NewDaily(cal statday.Calendar, interval time.Duration) *Daily {
	return &Daily{
		cal:      cal,
		interval: int64(interval / time.Second),
		dayStart: cal.DayStart(time.Now()),
		devices:  make(map[string]*DeviceTotals),
	}
}

// Name 实现 collector 的 sink 命名。
func (d *Daily) Name() string { return "daily-counter" }

// Restore 用 today.db 的汇总结果重建计数器。
// devices 为按设备汇总的行，domains 为按设备+域名汇总的行。
func (d *Daily) Restore(dayStart time.Time, devices, domains []model.UsageTotal) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dayStart = dayStart
	d.devices = make(map[string]*DeviceTotals, len(devices))
	for _, r := range devices {
		dt := d.deviceLocked(r.DeviceMAC)
		dt.Totals = Totals{BytesUp: r.BytesUp, BytesDown: r.BytesDown, ActiveSeconds: r.ActivePeriods * d.interval}
	}
	for _, r := range domains {
		d.deviceLocked(r.DeviceMAC).Domains[r.Domain] = Totals{
			BytesUp: r.BytesUp, BytesDown: r.BytesDown, ActiveSeconds: r.ActivePeriods * d.interval,
		}
	}
}

// Consume 累加一个采集周期的记录；跨过统计日边界时先清零。
func (d *Daily) Consume(_ context.Context, batch model.UsageBatch) error {
	start := d.cal.DayStart(time.Unix(batch.CollectedAt, 0))

	d.mu.Lock()
	defer d.mu.Unlock()
	if start.After(d.dayStart) {
		d.dayStart = start
		d.devices = make(map[string]*DeviceTotals)
	}

	seenDevice := make(map[string]bool)
	seenDomain := make(map[[2]string]bool)
	for i := range batch.Events {
		ev := &batch.Events[i]
		dt := d.deviceLocked(ev.DeviceMAC)
		dt.BytesUp += ev.BytesUp
		dt.BytesDown += ev.BytesDown
		if !seenDevice[ev.DeviceMAC] {
			seenDevice[ev.DeviceMAC] = true
			dt.ActiveSeconds += d.interval
		}

		dom := dt.Domains[ev.Domain]
		dom.BytesUp += ev.BytesUp
		dom.BytesDown += ev.BytesDown
		if k := [2]string{ev.DeviceMAC, ev.Domain}; !seenDomain[k] {
			seenDomain[k] = true
			dom.ActiveSeconds += d.interval
		}
		dt.Domains[ev.Domain] = dom
	}
	return nil
}

// DayStart 返回当前统计日的起始时刻。
func (d *Daily) DayStart() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.dayStart
}

// Device 返回某设备的当日累计值副本。
func (d *Daily) Device(mac string) (DeviceTotals, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	dt, ok := d.devices[mac]
	if !ok {
		return DeviceTotals{}, false
	}
	return DeviceTotals{Totals: dt.Totals, Domains: maps.Clone(dt.Domains)}, true
}

// Snapshot 返回全部设备当日累计值（不含域名明细）。
func (d *Daily) Snapshot() map[string]Totals {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make(map[string]Totals, len(d.devices))
	for mac, dt := range d.devices {
		out[mac] = dt.Totals
	}
	return out
}

func (d *Daily) deviceLocked(mac string) *DeviceTotals {
	dt := d.devices[mac]
	if dt == nil {
		dt = &DeviceTotals{Domains: make(map[string]Totals)}
		d.devices[mac] = dt
	}
	return dt
}
