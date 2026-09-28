package collector

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/collector/nftstats"
	"github.com/leo/leosentry/internal/model"
)

// TrafficReader 读取本周期的流量增量。
type TrafficReader interface {
	Read() (map[nftstats.FlowPair]nftstats.Traffic, error)
}

// DeviceDirectory 提供 IP→设备 换算。
type DeviceDirectory interface {
	RefreshNeighbors()
	Lookup(ip netip.Addr) (model.Device, bool)
}

// DomainResolver 提供 IP→域名 反查。
type DomainResolver interface {
	Lookup(ip netip.Addr) string
}

// Sink 消费每个采集周期产出的记录。
type Sink interface {
	Consume(ctx context.Context, batch model.UsageBatch) error
}

// Options 配置采集器。
type Options struct {
	Interval time.Duration
	// MinFlowBytes 为单个周期内一个 (设备, 目标) 组合上下行合计的最小字节数，
	// 低于该值的记录（心跳、推送保活等）直接丢弃，不写库也不计入当日计数与活跃时长；0 表示不过滤。
	MinFlowBytes int64
	Traffic      TrafficReader
	Devices      DeviceDirectory
	Domains      DomainResolver
	Sinks        []Sink
	Logger       *slog.Logger
}

// Collector 按固定周期读取 nft 流量计数，换算设备与域名后组装为 UsageEvent。
type Collector struct {
	opts    Options
	log     *slog.Logger
	mu      sync.Mutex
	every   time.Duration
	minFlow int64
	perMin  int64
	poke    chan struct{}
}

// New 创建采集器。
func New(opts Options) *Collector {
	c := &Collector{opts: opts, log: opts.Logger, every: opts.Interval, minFlow: opts.MinFlowBytes, poke: make(chan struct{}, 1)}
	if opts.Interval > 0 {
		c.perMin = opts.MinFlowBytes * int64(time.Minute) / int64(opts.Interval)
	}
	return c
}

// SetInterval 修改采集周期。下一次采集按新间隔对齐，过滤阈值按每分钟字节数折算。
func (c *Collector) SetInterval(d time.Duration) {
	if c == nil || d < time.Second || d > time.Hour {
		return
	}
	c.mu.Lock()
	c.every = d
	c.minFlow = c.perMin * int64(d) / int64(time.Minute)
	c.mu.Unlock()
	select {
	case c.poke <- struct{}{}:
	default:
	}
}

// Interval 返回当前采集周期。
func (c *Collector) Interval() time.Duration {
	if c == nil {
		return time.Minute
	}
	every, _ := c.pace()
	return every
}

func (c *Collector) pace() (time.Duration, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	every := c.every
	if every <= 0 {
		every = c.opts.Interval
	}
	return every, c.minFlow
}

// Run 在每个周期边界采集一次，直到 ctx 取消。
// 周期对齐到 Interval 的整数倍，使程序重启后同一周期的记录能按唯一键合并。
// 退出前补采一次不满一个周期的流量，写入时不受 ctx 取消影响，重启不丢数据。
func (c *Collector) Run(ctx context.Context) {
	every, minFlow := c.pace()
	c.log.Info("collector started", "interval", every, "min_flow_bytes", minFlow)
	timer := time.NewTimer(c.untilNextTick(time.Now()))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			every, _ := c.pace()
			// 归入当前所在周期（向上取整），与重启后该周期剩余部分的记录合并
			c.collect(context.WithoutCancel(ctx), time.Now().Truncate(every).Add(every))
			return
		case <-c.poke:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(c.untilNextTick(time.Now()))
		case now := <-timer.C:
			c.collect(context.WithoutCancel(ctx), now)
			timer.Reset(c.untilNextTick(time.Now()))
		}
	}
}

func (c *Collector) untilNextTick(now time.Time) time.Duration {
	every, _ := c.pace()
	if every <= 0 {
		every = time.Minute
	}
	return now.Truncate(every).Add(every).Sub(now)
}

func (c *Collector) collect(ctx context.Context, now time.Time) {
	every, minFlow := c.pace()
	if every <= 0 {
		every = time.Minute
	}
	collectedAt := now.Round(every).Unix()

	traffic, err := c.opts.Traffic.Read()
	if err != nil {
		c.log.Error("read traffic counters failed", "err", err)
		return
	}
	if len(traffic) == 0 {
		return
	}
	c.opts.Devices.RefreshNeighbors()

	batch := model.UsageBatch{
		CollectedAt: collectedAt,
		Events:      make([]model.UsageEvent, 0, len(traffic)),
	}
	dropped := 0
	for pair, t := range traffic {
		if int64(t.BytesUp+t.BytesDown) < minFlow {
			dropped++
			continue
		}
		deviceIP, targetIP := pair.DeviceAddr(), pair.TargetAddr()
		ev := model.UsageEvent{
			CollectedAt: collectedAt,
			DeviceIP:    deviceIP,
			TargetIP:    targetIP,
			Domain:      c.opts.Domains.Lookup(targetIP),
			BytesUp:     int64(t.BytesUp),
			BytesDown:   int64(t.BytesDown),
		}
		if d, ok := c.opts.Devices.Lookup(deviceIP); ok {
			ev.DeviceMAC = d.MAC
		}
		batch.Events = append(batch.Events, ev)
	}
	if len(batch.Events) == 0 {
		return
	}

	for _, s := range c.opts.Sinks {
		if err := s.Consume(ctx, batch); err != nil {
			c.log.Error("consume usage batch failed", "sink", sinkName(s), "err", err)
		}
	}
	c.log.Debug("collected", "events", len(batch.Events), "dropped_small", dropped, "cost", time.Since(now))
}

func sinkName(v any) string {
	type named interface{ Name() string }
	if n, ok := v.(named); ok {
		return n.Name()
	}
	return "unknown"
}
