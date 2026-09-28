package policy

import (
	"context"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/store"
)

// DayScanner 提供当天用量明细，由 store.Store 实现。
type DayScanner interface {
	ScanDay(ctx context.Context, begin func(dayStart time.Time), fn func(*store.UsageRow) error) error
}

// Usage 按给定设备统计今天的上网分钟和游戏分钟。
type Usage struct {
	mu       sync.RWMutex
	Scan     DayScanner
	Rules    *category.Matcher
	Interval time.Duration
}

// SetRules 替换统计用的规则。
func (u *Usage) SetRules(m *category.Matcher) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.Rules = m
	u.mu.Unlock()
}

// CurrentInterval 返回当前折算用的采集周期。
func (u *Usage) CurrentInterval() time.Duration {
	if u == nil {
		return time.Minute
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.Interval <= 0 {
		return time.Minute
	}
	return u.Interval
}

// SetInterval 替换把采集周期折算成分钟时使用的间隔。
func (u *Usage) SetInterval(d time.Duration) {
	if u == nil || d <= 0 {
		return
	}
	u.mu.Lock()
	u.Interval = d
	u.mu.Unlock()
}

// Minutes 只统计 macs 里的设备。没有设备时不扫描数据库。
func (u *Usage) Minutes(ctx context.Context, macs []string) (map[string]Minutes, error) {
	u.mu.RLock()
	rules, interval := u.Rules, u.Interval
	u.mu.RUnlock()
	return CollectMinutes(ctx, u.Scan, rules, macs, interval)
}

// CollectMinutes 把采集周期折算成分钟。同一周期里的多条流量只计一分钟。
func CollectMinutes(ctx context.Context, scan DayScanner, rules *category.Matcher, macs []string, interval time.Duration) (map[string]Minutes, error) {
	if len(macs) == 0 || scan == nil {
		return map[string]Minutes{}, nil
	}
	want := make(map[string]struct{}, len(macs))
	for _, m := range macs {
		if m != "" {
			want[m] = struct{}{}
		}
	}
	if len(want) == 0 {
		return map[string]Minutes{}, nil
	}
	gameCat, videoCats := -1, map[int]bool{}
	if rules != nil {
		gameCat = rules.CategoryIndex("game")
		for _, id := range []string{"video", "shortvideo", "live"} {
			if i := rules.CategoryIndex(id); i >= 0 {
				videoCats[i] = true
			}
		}
	}
	type periods struct {
		inet, game, video map[int64]struct{}
	}
	by := map[string]*periods{}
	err := scan.ScanDay(ctx, func(time.Time) {}, func(r *store.UsageRow) error {
		if _, ok := want[r.DeviceMAC]; !ok {
			return nil
		}
		p := by[r.DeviceMAC]
		if p == nil {
			p = &periods{inet: map[int64]struct{}{}, game: map[int64]struct{}{}, video: map[int64]struct{}{}}
			by[r.DeviceMAC] = p
		}
		p.inet[r.CollectedAt] = struct{}{}
		if rules != nil {
			var idx int
			var ok bool
			if r.Domain != "" {
				idx, ok = rules.Match(r.Domain)
			}
			if !ok {
				idx, ok = rules.MatchIP(r.TargetIP)
			}
			if ok {
				cat := rules.App(idx).Category
				if cat == gameCat {
					p.game[r.CollectedAt] = struct{}{}
				} else if videoCats[cat] {
					p.video[r.CollectedAt] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Minutes, len(by))
	for mac, p := range by {
		out[mac] = Minutes{
			Internet: toMinutes(len(p.inet), interval),
			Game:     toMinutes(len(p.game), interval),
			Video:    toMinutes(len(p.video), interval),
		}
	}
	return out, nil
}

func toMinutes(periods int, interval time.Duration) int {
	if interval <= 0 {
		interval = time.Minute
	}
	return int(int64(periods) * int64(interval) / int64(time.Minute))
}
