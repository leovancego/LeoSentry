package device

import (
	"context"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/model"
)

type memBook struct {
	byMAC map[string]*model.KnownDevice
	names map[string]string
	ver   uint64
}

func (m *memBook) Observe(mac string, ip netip.Addr, at int64) {
	if mac == "" {
		return
	}
	if m.byMAC == nil {
		m.byMAC = map[string]*model.KnownDevice{}
	}
	d := m.byMAC[mac]
	if d == nil {
		d = &model.KnownDevice{MAC: mac, FirstSeen: at, LastSeen: at}
		m.byMAC[mac] = d
	}
	if at < d.FirstSeen {
		d.FirstSeen = at
	}
	if at > d.LastSeen {
		d.LastSeen = at
	}
	if ip.IsValid() && ip.Is4() {
		d.LastIP = ip
		for i := range d.IPs {
			if d.IPs[i].IP == ip {
				if at < d.IPs[i].FirstSeen {
					d.IPs[i].FirstSeen = at
				}
				if at > d.IPs[i].LastSeen {
					d.IPs[i].LastSeen = at
				}
				return
			}
		}
		d.IPs = append(d.IPs, model.KnownIP{IP: ip, FirstSeen: at, LastSeen: at})
	}
}

func (m *memBook) Flush(context.Context) error { return nil }

func (m *memBook) SetName(_ context.Context, mac, name string) error {
	if m.names == nil {
		m.names = map[string]string{}
	}
	m.names[mac] = name
	if d := m.byMAC[mac]; d != nil {
		d.Name = name
	} else {
		m.Observe(mac, netip.Addr{}, time.Now().Unix())
		m.byMAC[mac].Name = name
	}
	m.ver++
	return nil
}

func (m *memBook) All() []model.KnownDevice {
	out := make([]model.KnownDevice, 0, len(m.byMAC))
	for _, d := range m.byMAC {
		out = append(out, *d)
	}
	return out
}

func (m *memBook) LookupName(mac string) string { return m.names[mac] }
func (m *memBook) Version() uint64              { return m.ver }

func TestCatalogListMergesLiveAndOnline(t *testing.T) {
	book := &memBook{}
	book.Observe("aa:bb:cc:00:00:01", netip.MustParseAddr("192.168.1.10"), 1000)
	live := fakeLive{{
		MAC: "aa:bb:cc:00:00:01", IP: netip.MustParseAddr("192.168.1.11"),
		Hostname: "kid-pad", Source: model.SourceLease,
	}}
	online := fakeOnline{{
		MAC: "aa:bb:cc:00:00:01", IP: netip.MustParseAddr("192.168.1.11"),
		Online: true, Flows: 3, LastActiveAt: time.Unix(2000, 0),
	}}
	c := NewCatalog(CatalogOptions{
		Book: book, Live: live, Online: online,
		Location: time.FixedZone("CST", 8*3600),
		Now:      func() time.Time { return time.Unix(3000, 0) },
	})
	list, err := c.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v %v", list, err)
	}
	e := list[0]
	if !e.Online || e.Hostname != "kid-pad" || e.IP != "192.168.1.11" || e.Flows != 3 || e.LastActiveAt != 2000 {
		t.Fatalf("entry = %+v", e)
	}
	if e.FirstSeen != 1000 || e.LastSeen != 3000 {
		t.Fatalf("seen = %d %d", e.FirstSeen, e.LastSeen)
	}
	if !slices.ContainsFunc(e.IPs, func(ip IPEntry) bool { return ip.IP == "192.168.1.11" && ip.Current }) {
		t.Fatalf("ips = %+v", e.IPs)
	}
}

func TestCatalogSetNameRejectsBadID(t *testing.T) {
	c := NewCatalog(CatalogOptions{Book: &memBook{}})
	if err := c.SetName(context.Background(), "not-a-mac", "x"); err != ErrInvalidID {
		t.Fatalf("err = %v", err)
	}
	if err := c.SetName(context.Background(), "aa-bb-cc-00-00-01", "平板"); err != nil {
		t.Fatal(err)
	}
}

type fakeLive []model.Device

func (f fakeLive) Devices() []model.Device { return f }

type fakeOnline []model.DeviceActivity

func (f fakeOnline) Activities() []model.DeviceActivity { return f }
