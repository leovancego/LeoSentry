package device

import (
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/leo/leosentry/internal/model"
)

const leases = `1727338000 AA:BB:CC:00:00:01 192.168.1.10 kid-ipad 01:aa:bb:cc:00:00:01
1727338000 aa:bb:cc:00:00:02 192.168.1.11 * *
duid 00:01:00:01:2c:5f:aa:bb
1727338000 1234 fd00::10 host6 000100
`

const dhcpConfig = `
config host
	option name 'nas'
	option mac 'aa:bb:cc:00:00:05'
	option ip '192.168.1.5'
`

const arp = `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.10     0x1         0x2         aa:bb:cc:00:00:01     *        br-lan
192.168.1.11     0x1         0x2         aa:bb:cc:00:00:99     *        br-lan
192.168.1.20     0x1         0x2         aa:bb:cc:00:00:02     *        br-lan
192.168.1.30     0x1         0x0         00:00:00:00:00:00     *        br-lan
`

func TestResolverMerge(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := NewResolver(Options{
		LeasesFile:     write("dhcp.leases", leases),
		DHCPConfigFile: write("dhcp", dhcpConfig),
		ARPFile:        write("arp", arp),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	r.reloadLeases()
	r.reloadStatic()
	r.RefreshNeighbors()

	check := func(ip, mac, host string, src model.DeviceSource) {
		t.Helper()
		d, ok := r.Lookup(netip.MustParseAddr(ip))
		if !ok {
			t.Fatalf("%s not found", ip)
		}
		if d.MAC != mac || d.Hostname != host || d.Source != src {
			t.Errorf("%s = %+v, want mac=%s host=%s src=%s", ip, d, mac, host, src)
		}
	}
	check("192.168.1.10", "aa:bb:cc:00:00:01", "kid-ipad", model.SourceLease)
	check("192.168.1.5", "aa:bb:cc:00:00:05", "nas", model.SourceStatic)
	// 租约与邻居表冲突时以邻居表为准
	check("192.168.1.11", "aa:bb:cc:00:00:99", "", model.SourceNeighbor)
	// 设备换了 IP：主机名按 MAC 从租约继承
	check("192.168.1.20", "aa:bb:cc:00:00:02", "", model.SourceNeighbor)

	if _, ok := r.Lookup(netip.MustParseAddr("192.168.1.30")); ok {
		t.Error("incomplete arp entry should be ignored")
	}
	if len(r.Devices()) != 4 {
		t.Errorf("devices = %d", len(r.Devices()))
	}
}
