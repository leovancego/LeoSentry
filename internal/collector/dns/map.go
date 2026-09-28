package dns

import (
	"net/netip"
	"sync"
	"time"
)

// pendingTTL 是 extra 模式下"流水号→查询域名"配对的保留时间，超过即认为该查询已结束。
const pendingTTL = time.Minute

type entry struct {
	domain   string
	lastSeen int64
}

type pendingQuery struct {
	domain string
	at     int64
}

// Map 维护 目标IP → 域名 的映射，条目在 ttl 内未被刷新即视为过期，
// 以避免 CDN 复用 IP 导致域名归属出错。只记录 IPv4，与 nft 计数维度一致。
type Map struct {
	ttl int64

	mu      sync.RWMutex
	entries map[netip.Addr]entry

	// 以下字段只由日志读取协程访问。
	pending map[uint64]pendingQuery
	// chainHead 用于无流水号格式下的 CNAME 链：记录链首域名，
	// 使 CNAME 末端解析出的 IP 归属到用户实际查询的域名。
	chainHead string
}

// NewMap 创建 DNS Map。
func NewMap(ttl time.Duration) *Map {
	return &Map{
		ttl:     int64(ttl / time.Second),
		entries: make(map[netip.Addr]entry),
		pending: make(map[uint64]pendingQuery),
	}
}

// Snapshot 返回尚未过期的 IPv4 → 域名，供策略把娱乐和游戏地址写入防火墙。
func (m *Map) Snapshot(now time.Time) map[netip.Addr]string {
	nowSec := now.Unix()
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[netip.Addr]string, len(m.entries))
	for ip, e := range m.entries {
		if nowSec-e.lastSeen <= m.ttl {
			out[ip] = e.domain
		}
	}
	return out
}

// Lookup 返回 IP 对应的域名，未知或已过期时返回空串。
func (m *Map) Lookup(ip netip.Addr) string {
	m.mu.RLock()
	e, ok := m.entries[ip]
	m.mu.RUnlock()
	if !ok || time.Now().Unix()-e.lastSeen > m.ttl {
		return ""
	}
	return e.domain
}

// Len 返回当前条目数（含尚未清理的过期条目）。
func (m *Map) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

// Sweep 删除过期条目。
func (m *Map) Sweep(now time.Time) {
	deadline := now.Unix() - m.ttl
	m.mu.Lock()
	for ip, e := range m.entries {
		if e.lastSeen < deadline {
			delete(m.entries, ip)
		}
	}
	m.mu.Unlock()

	pendingDeadline := now.Add(-pendingTTL).Unix()
	for k, q := range m.pending {
		if q.at < pendingDeadline {
			delete(m.pending, k)
		}
	}
}

// ingest 批量写入解析后的日志行，整批只加一次写锁。
func (m *Map) ingest(lines []logLine, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range lines {
		m.ingestOne(&lines[i], now)
	}
}

func (m *Map) ingestOne(l *logLine, now time.Time) {
	ts := now.Unix()
	if !l.Time.IsZero() && l.Time.Before(now) {
		ts = l.Time.Unix()
	}

	switch l.Kind {
	case kindQuery:
		m.chainHead = ""
		if l.HasSerial {
			if _, ok := m.pending[l.Serial]; !ok {
				m.pending[l.Serial] = pendingQuery{domain: l.Domain, at: ts}
			}
		}
	case kindAnswer:
		domain := l.Domain
		if l.HasSerial {
			if q, ok := m.pending[l.Serial]; ok {
				domain = q.domain
			}
		} else {
			if m.chainHead != "" {
				domain = m.chainHead
			}
			if l.Answer == "<CNAME>" && m.chainHead == "" {
				m.chainHead = l.Domain
			}
		}
		ip, err := netip.ParseAddr(l.Answer)
		if err != nil || !ip.Is4() {
			return
		}
		if e, ok := m.entries[ip]; ok && e.lastSeen > ts {
			return
		}
		m.entries[ip] = entry{domain: domain, lastSeen: ts}
	}
}
