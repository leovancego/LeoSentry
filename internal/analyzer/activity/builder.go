package activity

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/statday"
	"github.com/leo/leosentry/internal/store"
)

const (
	bucketSeconds = 600
	recentSeconds = 300
	// maxCacheAge 限制缓存寿命：明细每个采集周期才变化一次，
	// 但在线状态（conntrack）每 10 秒刷新，过期后重新聚合以反映最新在线情况。
	maxCacheAge = 20 * time.Second
	maxHints    = 2
)

// UsageSource 提供当天明细，由 store.Store 实现。
type UsageSource interface {
	ScanDay(ctx context.Context, begin func(dayStart time.Time), fn func(*store.UsageRow) error) error
	Version() uint64
}

// DeviceDirectory 提供设备主机名，由 device.Resolver 实现。
type DeviceDirectory interface {
	Devices() []model.Device
}

// NameSource 提供用户给设备起的名字，由 store.Registry 实现。
type NameSource interface {
	LookupName(mac string) string
	Version() uint64
}

// OnlineSource 提供设备实时在线状态，由 conntrack.Tracker 实现。
type OnlineSource interface {
	Activities() []model.DeviceActivity
}

// RuleSource 提供当前分类规则。*category.Matcher 和 *category.Live 都实现它。
type RuleSource interface {
	Get() *category.Matcher
}

// Options 配置概览生成器。
type Options struct {
	Usage   UsageSource
	Devices DeviceDirectory
	// Names 可为 nil（未启用设备命名时）。
	Names NameSource
	// Online 可为 nil（conntrack 不可用时）。
	Online                OnlineSource
	Rules                 RuleSource
	Calendar              statday.Calendar
	Interval              time.Duration
	MinFlowBytesPerMinute int64
	Logger                *slog.Logger

	now func() time.Time
}

// Snapshot 是一次聚合的序列化结果，JSON 与 gzip 压缩版本各生成一次，之后直接复用。
type Snapshot struct {
	ETag    string
	JSON    []byte
	Gzip    []byte
	Created time.Time
}

// Builder 按需生成今日概览并缓存：数据版本未变且缓存未过期时直接返回缓存，
// 并发请求串行化，同一时刻最多进行一次聚合。
type Builder struct {
	opts Options

	mu      sync.Mutex
	cached  map[string]*Snapshot
	version map[string]uint64
	nameVer map[string]uint64
}

// NewBuilder 创建概览生成器。
func NewBuilder(opts Options) *Builder {
	if opts.now == nil {
		opts.now = time.Now
	}
	return &Builder{
		opts: opts, cached: map[string]*Snapshot{}, version: map[string]uint64{}, nameVer: map[string]uint64{},
	}
}

// Snapshot 返回今天或昨天的概览快照。which 不是 yesterday 时按今天处理。
func (b *Builder) Snapshot(ctx context.Context, which string) (*Snapshot, error) {
	if which != "yesterday" {
		which = "today"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	version := b.opts.Usage.Version()
	var nameVer uint64
	if b.opts.Names != nil {
		nameVer = b.opts.Names.Version()
	}
	now := b.opts.now()
	if snap := b.cached[which]; snap != nil && b.version[which] == version && b.nameVer[which] == nameVer && now.Sub(snap.Created) < maxCacheAge {
		return snap, nil
	}
	ov, err := b.build(ctx, which)
	if err != nil {
		return nil, err
	}
	snap, err := encode(ov, version, now)
	if err != nil {
		return nil, err
	}
	b.cached[which], b.version[which], b.nameVer[which] = snap, version, nameVer
	if b.opts.Logger != nil {
		b.opts.Logger.Debug("overview built", "day", which, "rows", ov.Stats.Rows, "devices", len(ov.Devices),
			"apps", len(ov.Apps), "cost_ms", ov.Stats.BuildMillis, "json_bytes", len(snap.JSON), "gzip_bytes", len(snap.Gzip))
	}
	return snap, nil
}

func encode(ov *Overview, version uint64, now time.Time) (*Snapshot, error) {
	raw, err := json.Marshal(ov)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	zw.Write(raw)
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return &Snapshot{
		ETag:    `"` + strconv.FormatUint(version, 36) + "-" + strconv.FormatInt(now.UnixMilli(), 36) + `"`,
		JSON:    raw,
		Gzip:    buf.Bytes(),
		Created: now,
	}, nil
}

// counter 统计去重后的活跃周期数与字节数。明细按 collected_at 升序到达，
// 同一周期的多行只计一个周期。
type counter struct {
	periods  int64
	up, down int64
	last     int64
}

func (c *counter) hit(at, up, down int64) {
	if c.last != at {
		c.last = at
		c.periods++
	}
	c.up += up
	c.down += down
}

type counters[K comparable] map[K]*counter

func (m counters[K]) hit(k K, at, up, down int64) {
	c := m[k]
	if c == nil {
		c = &counter{}
		m[k] = c
	}
	c.hit(at, up, down)
}

type devKey struct{ d, b int }
type catKey struct{ d, b, c int }
type appKey struct{ d, b, a int }
type recentKey struct{ d, a int }

type deviceAcc struct {
	info  DeviceInfo
	hints map[string]int64
}

// aggregation 是一次 Build 的中间状态。
type aggregation struct {
	rules      *category.Matcher
	cats       []category.Category
	otherCat   int
	unknownCat int

	devices   []*deviceAcc
	deviceIdx map[string]int
	apps      []AppInfo
	appIdx    map[string]int
	appMemo   map[string]int

	dev    counters[devKey]
	cat    counters[catKey]
	ent    counters[devKey]
	app    counters[appKey]
	recent counters[recentKey]
}

func (g *aggregation) appFor(domain string, ip netip.Addr) int {
	memo := domain
	if memo == "" {
		memo = "ip:" + ip.String()
	}
	if i, ok := g.appMemo[memo]; ok {
		return i
	}
	var key string
	var info AppInfo
	var ri int
	var matched bool
	if domain != "" && g.rules != nil {
		ri, matched = g.rules.Match(domain)
	}
	if !matched && g.rules != nil {
		ri, matched = g.rules.MatchIP(ip)
	}
	if domain == "" && !matched {
		key, info = "unknown", AppInfo{Name: "未识别流量", Category: g.unknownCat}
	} else if matched {
		app := g.rules.App(ri)
		key, info = "rule:"+app.Name, AppInfo{Name: app.Name, Category: app.Category, Rule: true}
	} else {
		site := category.RegistrableDomain(domain)
		key, info = "site:"+site, AppInfo{Name: site, Category: g.otherCat}
	}
	i, ok := g.appIdx[key]
	if !ok {
		i = len(g.apps)
		g.appIdx[key] = i
		g.apps = append(g.apps, info)
	}
	g.appMemo[memo] = i
	return i
}

func (g *aggregation) deviceFor(r *store.UsageRow) int {
	id := cmp.Or(r.DeviceMAC, r.DeviceIP.String())
	i, ok := g.deviceIdx[id]
	if !ok {
		i = len(g.devices)
		g.deviceIdx[id] = i
		g.devices = append(g.devices, &deviceAcc{
			info:  DeviceInfo{ID: id, MAC: r.DeviceMAC, FirstSeen: r.CollectedAt},
			hints: make(map[string]int64),
		})
	}
	acc := g.devices[i]
	acc.info.IP = r.DeviceIP.String()
	acc.info.LastSeen = r.CollectedAt
	return i
}

// Build 扫描当前统计日并生成概览，不经过缓存。
func (b *Builder) Build(ctx context.Context) (*Overview, error) {
	return b.build(ctx, "today")
}

func (b *Builder) build(ctx context.Context, which string) (*Overview, error) {
	started := time.Now()
	now := b.opts.now()
	interval := int64(b.opts.Interval / time.Second)
	var rules *category.Matcher
	if b.opts.Rules != nil {
		rules = b.opts.Rules.Get()
	}
	g := &aggregation{
		rules:      rules,
		cats:       rules.Categories(),
		otherCat:   rules.CategoryIndex(category.Other),
		unknownCat: rules.CategoryIndex(category.Unknown),
		deviceIdx:  make(map[string]int),
		appIdx:     make(map[string]int),
		appMemo:    make(map[string]int),
		dev:        make(counters[devKey]),
		cat:        make(counters[catKey]),
		ent:        make(counters[devKey]),
		app:        make(counters[appKey]),
		recent:     make(counters[recentKey]),
	}
	recentFrom := now.Unix() - recentSeconds

	var (
		start    time.Time
		dayStart int64
		buckets  int
		lastAt   int64
		stats    Stats
	)
	scan := b.opts.Usage.ScanDay
	if which == "yesterday" {
		if ys, ok := b.opts.Usage.(interface {
			ScanYesterday(context.Context, func(time.Time), func(*store.UsageRow) error) error
		}); ok {
			scan = ys.ScanYesterday
		}
	}
	err := scan(ctx, func(ds time.Time) {
		start, dayStart = ds, ds.Unix()
		buckets = int((b.opts.Calendar.NextStart(ds).Unix() - dayStart + bucketSeconds - 1) / bucketSeconds)
	}, func(r *store.UsageRow) error {
		stats.Rows++
		if r.DomainInferred {
			stats.InferredRows++
		}
		if r.Domain == "" {
			stats.UnknownRows++
		}
		d := g.deviceFor(r)
		a := g.appFor(r.Domain, r.TargetIP)
		c := g.apps[a].Category
		// collected_at 是周期的结束时刻，按周期起点归入时段
		bk := max(0, min(int((r.CollectedAt-interval-dayStart)/bucketSeconds), buckets-1))
		at, up, down := r.CollectedAt, r.BytesUp, r.BytesDown

		g.dev.hit(devKey{d, bk}, at, up, down)
		g.cat.hit(catKey{d, bk, c}, at, up, down)
		if g.cats[c].Entertainment {
			g.ent.hit(devKey{d, bk}, at, up, down)
		}
		g.app.hit(appKey{d, bk, a}, at, up, down)
		if at > recentFrom {
			g.recent.hit(recentKey{d, a}, at, up, down)
		}
		if h, ok := rules.Hint(r.Domain); ok {
			g.devices[d].hints[h] += up + down
		}
		lastAt = max(lastAt, at)
		return nil
	})
	if err != nil {
		return nil, err
	}

	ov := &Overview{
		GeneratedAt:           now.Unix(),
		Which:                 which,
		Day:                   b.opts.Calendar.Label(start),
		DayStart:              dayStart,
		DayEnd:                b.opts.Calendar.NextStart(start).Unix(),
		IntervalSeconds:       int(interval),
		BucketSeconds:         bucketSeconds,
		Buckets:               buckets,
		LastCollectedAt:       lastAt,
		RecentSeconds:         recentSeconds,
		MinFlowBytesPerMinute: b.opts.MinFlowBytesPerMinute,
		Apps:                  g.apps,
		Stats:                 stats,
	}
	_, ov.TZOffset = start.In(b.opts.Calendar.Location()).Zone()
	for _, c := range g.cats {
		ov.Categories = append(ov.Categories, CategoryInfo{ID: c.ID, Name: c.Name, Entertainment: c.Entertainment})
	}
	ov.Devices = b.devices(g, which == "today")

	ov.DeviceBuckets = rowsOf(g.dev, func(k devKey, c *counter) [5]int64 {
		return [5]int64{int64(k.d), int64(k.b), c.periods, c.up, c.down}
	})
	ov.CategoryBuckets = rowsOf(g.cat, func(k catKey, c *counter) [4]int64 {
		return [4]int64{int64(k.d), int64(k.b), int64(k.c), c.periods}
	})
	ov.EntBuckets = rowsOf(g.ent, func(k devKey, c *counter) [3]int64 {
		return [3]int64{int64(k.d), int64(k.b), c.periods}
	})
	ov.AppBuckets = rowsOf(g.app, func(k appKey, c *counter) [6]int64 {
		return [6]int64{int64(k.d), int64(k.b), int64(k.a), c.periods, c.up, c.down}
	})
	ov.Recent = rowsOf(g.recent, func(k recentKey, c *counter) [3]int64 {
		return [3]int64{int64(k.d), int64(k.a), c.periods}
	})
	ov.Stats.BuildMillis = time.Since(started).Milliseconds()
	if ov.Categories == nil {
		ov.Categories = []CategoryInfo{}
	}
	if ov.Apps == nil {
		ov.Apps = []AppInfo{}
	}
	if ov.Devices == nil {
		ov.Devices = []DeviceInfo{}
	}
	return ov, nil
}

// rowsOf 把计数表转为按前三列排序的紧凑数组，保证输出稳定。
func rowsOf[K comparable, R [3]int64 | [4]int64 | [5]int64 | [6]int64](m counters[K], row func(K, *counter) R) []R {
	out := make([]R, 0, len(m))
	for k, c := range m {
		out = append(out, row(k, c))
	}
	slices.SortFunc(out, func(a, b R) int {
		for i := range 3 {
			if c := cmp.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
		return 0
	})
	return out
}

// devices 补全主机名与设备类型线索，并合并 conntrack 实时状态：
// 当前在线但今天流量未达到记录阈值的设备也会列出。
func (b *Builder) devices(g *aggregation, live bool) []DeviceInfo {
	hostnames := make(map[string]string)
	for _, d := range b.opts.Devices.Devices() {
		if d.Hostname != "" {
			hostnames[d.MAC] = d.Hostname
			hostnames[d.IP.String()] = d.Hostname
		}
	}
	if live && b.opts.Online != nil {
		for _, a := range b.opts.Online.Activities() {
			id := cmp.Or(a.MAC, a.IP.String())
			i, ok := g.deviceIdx[id]
			if !ok {
				if !a.Online {
					continue
				}
				i = len(g.devices)
				g.deviceIdx[id] = i
				g.devices = append(g.devices, &deviceAcc{info: DeviceInfo{ID: id, MAC: a.MAC, IP: a.IP.String()}})
			}
			info := &g.devices[i].info
			info.Online, info.Flows = a.Online, a.Flows
			if !a.LastActiveAt.IsZero() {
				info.LastActiveAt = a.LastActiveAt.Unix()
			}
			info.Hostname = cmp.Or(info.Hostname, a.Hostname)
		}
	}

	out := make([]DeviceInfo, 0, len(g.devices))
	for _, acc := range g.devices {
		info := acc.info
		info.Hostname = cmp.Or(info.Hostname, hostnames[info.MAC], hostnames[info.IP])
		if b.opts.Names != nil && info.MAC != "" {
			info.Name = b.opts.Names.LookupName(info.MAC)
		}
		info.Hints = topHints(acc.hints)
		out = append(out, info)
	}
	return out
}

func topHints(h map[string]int64) []string {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for n := range h {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Or(cmp.Compare(h[b], h[a]), cmp.Compare(a, b)) })
	return names[:min(len(names), maxHints)]
}
