package conntrack

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/model"
)

// forgetAfter 之后仍无连接的设备从状态表中移除。
const forgetAfter = 24 * time.Hour

// flowKey 以原方向五元组标识一条连接，netlink 与 procfs 两种来源通用。
type flowKey struct {
	proto        uint8
	src, dst     [4]byte
	sport, dport uint16
}

// Source 提供 conntrack 表快照。
type Source interface {
	// Snapshot 把原方向源地址满足 keep 的 IPv4 连接写入 dst。
	Snapshot(dst map[flowKey]struct{}, keep func(src [4]byte) bool) error
	Name() string
	Close() error
}

// DeviceLookup 用于把设备 IP 换算为 MAC 与主机名。
type DeviceLookup interface {
	Lookup(ip netip.Addr) (model.Device, bool)
}

// Tracker 周期性地对 conntrack 表做快照并与上次差分，
// 得到每台受管设备的连接数、新建/结束连接数，用于 Web 面板的实时在线/活跃展示。
// 这些状态只存在于内存，不进入历史记录。
type Tracker struct {
	src      Source
	interval time.Duration
	managed  []netip.Prefix
	ignored  map[[4]byte]struct{}
	devices  DeviceLookup
	log      *slog.Logger

	prev, cur map[flowKey]struct{}

	mu     sync.RWMutex
	status map[[4]byte]*model.DeviceActivity
}

// NewTracker 创建 Tracker。
func NewTracker(src Source, interval time.Duration, managed []netip.Prefix, devices DeviceLookup, log *slog.Logger) *Tracker {
	return &Tracker{
		src:      src,
		interval: interval,
		managed:  managed,
		devices:  devices,
		log:      log,
		prev:     make(map[flowKey]struct{}),
		cur:      make(map[flowKey]struct{}),
		status:   make(map[[4]byte]*model.DeviceActivity),
	}
}

// Ignore 忽略以这些地址为源的连接，用于排除路由器自身在 LAN 上的地址。须在 Run 之前调用。
func (t *Tracker) Ignore(addrs ...netip.Addr) {
	if t.ignored == nil {
		t.ignored = make(map[[4]byte]struct{}, len(addrs))
	}
	for _, a := range addrs {
		if a.Is4() {
			t.ignored[a.As4()] = struct{}{}
		}
	}
}

// Run 周期性采集直到 ctx 取消。
func (t *Tracker) Run(ctx context.Context) {
	defer t.src.Close()
	t.log.Info("conntrack tracker started", "source", t.src.Name(), "interval", t.interval)

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		t.tick(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Activities 返回全部受管设备的当前状态，按 IP 排序。
func (t *Tracker) Activities() []model.DeviceActivity {
	t.mu.RLock()
	out := make([]model.DeviceActivity, 0, len(t.status))
	for _, a := range t.status {
		out = append(out, *a)
	}
	t.mu.RUnlock()
	slices.SortFunc(out, func(a, b model.DeviceActivity) int { return a.IP.Compare(b.IP) })
	return out
}

func (t *Tracker) tick(now time.Time) {
	clear(t.cur)
	if err := t.src.Snapshot(t.cur, t.isManaged); err != nil {
		t.log.Warn("conntrack snapshot failed", "source", t.src.Name(), "err", err)
		return
	}

	type counts struct{ flows, added, ended int }
	per := make(map[[4]byte]*counts)
	get := func(ip [4]byte) *counts {
		c := per[ip]
		if c == nil {
			c = &counts{}
			per[ip] = c
		}
		return c
	}
	for k := range t.cur {
		c := get(k.src)
		c.flows++
		if _, ok := t.prev[k]; !ok {
			c.added++
		}
	}
	for k := range t.prev {
		if _, ok := t.cur[k]; !ok {
			get(k.src).ended++
		}
	}

	t.mu.Lock()
	for ip, c := range per {
		a := t.status[ip]
		if a == nil {
			a = &model.DeviceActivity{IP: netip.AddrFrom4(ip)}
			t.status[ip] = a
		}
		a.Flows, a.NewFlows, a.EndedFlows = c.flows, c.added, c.ended
		a.Online = c.flows > 0
		if c.added > 0 || c.flows > 0 {
			a.LastActiveAt = now
		}
		a.UpdatedAt = now
	}
	for ip, a := range t.status {
		if _, ok := per[ip]; !ok {
			a.Flows, a.NewFlows, a.EndedFlows, a.Online = 0, 0, 0, false
			a.UpdatedAt = now
			if now.Sub(a.LastActiveAt) > forgetAfter {
				delete(t.status, ip)
				continue
			}
		}
		if d, ok := t.devices.Lookup(a.IP); ok {
			a.MAC, a.Hostname = d.MAC, d.Hostname
		}
	}
	t.mu.Unlock()

	t.prev, t.cur = t.cur, t.prev
}

func (t *Tracker) isManaged(src [4]byte) bool {
	if _, ok := t.ignored[src]; ok {
		return false
	}
	ip := netip.AddrFrom4(src)
	for _, p := range t.managed {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
