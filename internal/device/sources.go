package device

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"strings"

	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/uci"
)

// atfComplete 是 /proc/net/arp 中 Flags 的 ATF_COM 位，表示邻居条目已解析出 MAC。
const atfComplete = 0x2

// parseLeases 解析 dnsmasq 租约文件，每行格式为：
// 到期时间 MAC IP 主机名 client-id。IPv6 租约与 duid 行被忽略。
func parseLeases(r io.Reader) map[netip.Addr]model.Device {
	out := make(map[netip.Addr]model.Device)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		ip, err := netip.ParseAddr(f[2])
		if err != nil || !ip.Is4() {
			continue
		}
		mac, ok := normalizeMAC(f[1])
		if !ok {
			continue
		}
		host := f[3]
		if host == "*" {
			host = ""
		}
		out[ip] = model.Device{MAC: mac, IP: ip, Hostname: host, Source: model.SourceLease}
	}
	return out
}

// parseStaticHosts 从 /etc/config/dhcp 中提取 config host 静态绑定。
// 一个 host 段的 mac 选项可能包含空格分隔的多个 MAC，取第一个作为身份。
func parseStaticHosts(f *uci.File) map[netip.Addr]model.Device {
	out := make(map[netip.Addr]model.Device)
	for _, s := range f.ByType("host") {
		ip, err := netip.ParseAddr(s.Get("ip"))
		if err != nil || !ip.Is4() {
			continue
		}
		var mac string
		for _, v := range s.List("mac") {
			for _, m := range strings.Fields(v) {
				if n, ok := normalizeMAC(m); ok {
					mac = n
					break
				}
			}
			if mac != "" {
				break
			}
		}
		if mac == "" {
			continue
		}
		out[ip] = model.Device{MAC: mac, IP: ip, Hostname: s.Get("name"), Source: model.SourceStatic}
	}
	return out
}

// parseProcARP 解析 /proc/net/arp，只保留已解析出 MAC 的 IPv4 邻居。
func parseProcARP(r io.Reader) map[netip.Addr]string {
	out := make(map[netip.Addr]string)
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		ip, err := netip.ParseAddr(f[0])
		if err != nil || !ip.Is4() {
			continue
		}
		if !hasFlag(f[2], atfComplete) {
			continue
		}
		mac, ok := normalizeMAC(f[3])
		if !ok {
			continue
		}
		out[ip] = mac
	}
	return out
}

func hasFlag(hex string, flag uint64) bool {
	var v uint64
	for _, c := range strings.TrimPrefix(hex, "0x") {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | uint64(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | uint64(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | uint64(c-'A'+10)
		default:
			return false
		}
	}
	return v&flag != 0
}

// NormalizeMAC 把各种写法（冒号、连字符、无分隔）规范为小写冒号形式。
func NormalizeMAC(s string) (string, bool) {
	return normalizeMAC(s)
}

func normalizeMAC(s string) (string, bool) {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", false
	}
	mac := hw.String()
	if mac == "00:00:00:00:00:00" {
		return "", false
	}
	return mac, true
}
