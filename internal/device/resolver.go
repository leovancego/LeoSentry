package device

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leo/leosentry/internal/fswatch"
	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/uci"
)

const (
	leasesFallbackInterval = 60 * time.Second
	staticFallbackInterval = 10 * time.Minute
)

// Options 配置 Resolver 的数据来源。
type Options struct {
	LeasesFile     string
	DHCPConfigFile string
	ARPFile        string
	// Watcher 可选，为 nil 时只依赖定时兜底刷新。
	Watcher *fswatch.Watcher
	Logger  *slog.Logger
}

type table = map[netip.Addr]model.Device

// Resolver 维护 IP→设备 对照表。
//
// 合并规则：静态绑定 < DHCP 租约 < 内核邻居表。租约与静态绑定提供主机名，
// 并覆盖暂时没有通信的设备；同一 IP 在邻居表中有已解析条目时以邻居表的 MAC 为准，
// 因为它反映内核实际与之通信的二层地址（租约可能已过时）。
//
// 查询走原子指针指向的只读快照，采集热路径上不加锁。
type Resolver struct {
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	leases table
	static table
	neigh  map[netip.Addr]string

	current atomic.Pointer[table]
}

// NewResolver 创建 Resolver，需调用 Start 加载数据。
func NewResolver(opts Options) *Resolver {
	r := &Resolver{
		opts:   opts,
		log:    opts.Logger,
		leases: table{},
		static: table{},
		neigh:  map[netip.Addr]string{},
	}
	empty := table{}
	r.current.Store(&empty)
	return r
}

// Start 同步加载全部来源，并启动后台刷新。
func (r *Resolver) Start(ctx context.Context) {
	r.reloadLeases()
	r.reloadStatic()
	r.RefreshNeighbors()

	go r.follow(ctx, r.opts.LeasesFile, leasesFallbackInterval, r.reloadLeases)
	go r.follow(ctx, r.opts.DHCPConfigFile, staticFallbackInterval, r.reloadStatic)
}

// Lookup 按 IP 查询设备。
func (r *Resolver) Lookup(ip netip.Addr) (model.Device, bool) {
	d, ok := (*r.current.Load())[ip]
	return d, ok
}

// Devices 返回当前已知的全部设备，按 IP 排序。
func (r *Resolver) Devices() []model.Device {
	t := *r.current.Load()
	out := slices.Collect(maps.Values(t))
	slices.SortFunc(out, func(a, b model.Device) int { return a.IP.Compare(b.IP) })
	return out
}

// RefreshNeighbors 重新读取内核邻居表，由采集器每个周期调用一次。
// 邻居表未变化时不重建快照。
func (r *Resolver) RefreshNeighbors() {
	data, err := os.ReadFile(r.opts.ARPFile)
	if err != nil {
		r.log.Debug("read arp table failed", "err", err)
		return
	}
	neigh := parseProcARP(bytes.NewReader(data))

	r.mu.Lock()
	defer r.mu.Unlock()
	if maps.Equal(neigh, r.neigh) {
		return
	}
	r.neigh = neigh
	r.rebuildLocked()
}

func (r *Resolver) reloadLeases() {
	data, err := os.ReadFile(r.opts.LeasesFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Warn("read dhcp leases failed", "file", r.opts.LeasesFile, "err", err)
		return
	}
	leases := parseLeases(bytes.NewReader(data))

	r.mu.Lock()
	defer r.mu.Unlock()
	r.leases = leases
	r.rebuildLocked()
}

func (r *Resolver) reloadStatic() {
	f, err := uci.ParseFile(r.opts.DHCPConfigFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("read dhcp config failed", "file", r.opts.DHCPConfigFile, "err", err)
		}
		return
	}
	static := parseStaticHosts(f)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.static = static
	r.rebuildLocked()
}

func (r *Resolver) rebuildLocked() {
	t := make(table, len(r.static)+len(r.leases)+len(r.neigh))
	hostByMAC := make(map[string]string, len(r.static)+len(r.leases))
	for ip, d := range r.static {
		t[ip] = d
		if d.Hostname != "" {
			hostByMAC[d.MAC] = d.Hostname
		}
	}
	for ip, d := range r.leases {
		t[ip] = d
		if d.Hostname != "" {
			hostByMAC[d.MAC] = d.Hostname
		}
	}
	for ip, mac := range r.neigh {
		if d, ok := t[ip]; ok && d.MAC == mac {
			continue
		}
		t[ip] = model.Device{MAC: mac, IP: ip, Hostname: hostByMAC[mac], Source: model.SourceNeighbor}
	}
	r.current.Store(&t)
}

func (r *Resolver) follow(ctx context.Context, path string, fallback time.Duration, reload func()) {
	var changed <-chan struct{}
	if r.opts.Watcher != nil {
		ch, err := r.opts.Watcher.Subscribe(path)
		if err != nil {
			r.log.Warn("watch file failed, falling back to polling", "file", path, "err", err)
		} else {
			changed = ch
		}
	}
	ticker := time.NewTicker(fallback)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-ticker.C:
		}
		reload()
	}
}
