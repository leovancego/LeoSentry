package device

import (
	"cmp"
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/model"
)

// ErrInvalidID 表示路径中的设备标识不是合法 MAC。
var ErrInvalidID = errors.New("invalid device id")

// Book 是持久的设备身份账本，由 store.Registry 实现。
type Book interface {
	Observe(mac string, ip netip.Addr, at int64)
	Flush(ctx context.Context) error
	SetName(ctx context.Context, mac, name string) error
	All() []model.KnownDevice
	LookupName(mac string) string
	Version() uint64
}

// LiveDevices 提供当前 DHCP / 邻居表中的设备。
type LiveDevices interface {
	Devices() []model.Device
}

// OnlineSource 提供 conntrack 实时在线状态。
type OnlineSource interface {
	Activities() []model.DeviceActivity
}

// Catalog 把持久身份、当前发现和在线状态合并成设备管理页要用的列表。
type Catalog struct {
	book   Book
	live   LiveDevices
	online OnlineSource
	loc    *time.Location
	now    func() time.Time
}

// CatalogOptions 配置设备目录。
type CatalogOptions struct {
	Book     Book
	Live     LiveDevices
	Online   OnlineSource
	Location *time.Location
	Now      func() time.Time
}

// NewCatalog 创建设备目录。
func NewCatalog(opts CatalogOptions) *Catalog {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	return &Catalog{book: opts.Book, live: opts.Live, online: opts.Online, loc: opts.Location, now: opts.Now}
}

// Entry 是设备管理页上的一台设备。
type Entry struct {
	ID           string    `json:"id"`
	MAC          string    `json:"mac"`
	Name         string    `json:"name,omitempty"`
	Hostname     string    `json:"hostname,omitempty"`
	IP           string    `json:"ip,omitempty"`
	Source       string    `json:"source,omitempty"`
	Online       bool      `json:"online"`
	Flows        int       `json:"flows,omitempty"`
	FirstSeen    int64     `json:"firstSeen,omitempty"`
	LastSeen     int64     `json:"lastSeen,omitempty"`
	LastActiveAt int64     `json:"lastActiveAt,omitempty"`
	IPs          []IPEntry `json:"ips,omitempty"`
}

// IPEntry 是历史上用过的一个地址。
type IPEntry struct {
	IP        string `json:"ip"`
	FirstSeen int64  `json:"firstSeen"`
	LastSeen  int64  `json:"lastSeen"`
	Current   bool   `json:"current,omitempty"`
}

type extra struct {
	hostname   string
	ip         string
	source     string
	online     bool
	flows      int
	lastActive int64
}

// List 合并账本、当前发现与在线状态。新出现的设备会写入账本。
func (c *Catalog) List(ctx context.Context) ([]Entry, error) {
	now := c.now()
	at := now.Unix()
	extras := map[string]*extra{}

	if c.live != nil {
		for _, d := range c.live.Devices() {
			if d.MAC == "" {
				continue
			}
			c.book.Observe(d.MAC, d.IP, at)
			e := extras[d.MAC]
			if e == nil {
				e = &extra{}
				extras[d.MAC] = e
			}
			e.hostname = d.Hostname
			if d.IP.IsValid() {
				e.ip = d.IP.String()
			}
			e.source = d.Source.String()
		}
	}
	if c.online != nil {
		for _, a := range c.online.Activities() {
			id := a.MAC
			if id == "" {
				continue
			}
			if a.IP.IsValid() {
				c.book.Observe(id, a.IP, at)
			}
			e := extras[id]
			if e == nil {
				e = &extra{}
				extras[id] = e
			}
			e.online = a.Online
			e.flows = a.Flows
			if !a.LastActiveAt.IsZero() {
				e.lastActive = a.LastActiveAt.Unix()
			}
			if e.ip == "" && a.IP.IsValid() {
				e.ip = a.IP.String()
			}
			if e.hostname == "" {
				e.hostname = a.Hostname
			}
		}
	}
	if err := c.book.Flush(ctx); err != nil {
		return nil, err
	}

	known := c.book.All()
	out := make([]Entry, 0, len(known))
	for _, k := range known {
		ent := Entry{
			ID:        k.MAC,
			MAC:       k.MAC,
			Name:      k.Name,
			FirstSeen: k.FirstSeen,
			LastSeen:  k.LastSeen,
		}
		if x := extras[k.MAC]; x != nil {
			ent.Hostname = x.hostname
			ent.IP = x.ip
			ent.Source = x.source
			ent.Online = x.online
			ent.Flows = x.flows
			ent.LastActiveAt = x.lastActive
			if x.lastActive > ent.LastSeen {
				ent.LastSeen = x.lastActive
			}
		}
		if ent.IP == "" && k.LastIP.IsValid() && !k.LastIP.IsUnspecified() {
			ent.IP = k.LastIP.String()
		}
		for _, ip := range k.IPs {
			ent.IPs = append(ent.IPs, IPEntry{
				IP:        ip.IP.String(),
				FirstSeen: ip.FirstSeen,
				LastSeen:  ip.LastSeen,
				Current:   ent.IP != "" && ip.IP.String() == ent.IP,
			})
		}
		if ent.IP != "" && !slices.ContainsFunc(ent.IPs, func(x IPEntry) bool { return x.IP == ent.IP }) {
			ent.IPs = append([]IPEntry{{IP: ent.IP, FirstSeen: at, LastSeen: at, Current: true}}, ent.IPs...)
		}
		out = append(out, ent)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		if n := cmp.Compare(b.LastSeen, a.LastSeen); n != 0 {
			return n
		}
		return strings.Compare(entryLabel(a), entryLabel(b))
	})
	return out, nil
}

// SetName 给设备起名或清空名字。
func (c *Catalog) SetName(ctx context.Context, id, name string) error {
	mac, ok := NormalizeMAC(id)
	if !ok {
		return ErrInvalidID
	}
	return c.book.SetName(ctx, mac, name)
}

// LookupName 返回用户别名。
func (c *Catalog) LookupName(mac string) string { return c.book.LookupName(mac) }

// Version 在改名后变化。
func (c *Catalog) Version() uint64 { return c.book.Version() }

// TZOffset 返回路由器时区相对 UTC 的秒数。
func (c *Catalog) TZOffset(t time.Time) int {
	_, off := t.In(c.loc).Zone()
	return off
}

func entryLabel(e Entry) string {
	return cmp.Or(e.Name, e.Hostname, e.IP, e.MAC)
}
