package sysconf

import (
	"fmt"
	"net"
	"net/netip"
)

// LANAddrs 返回网络接口上配置的全部 IPv4 地址，用作 Web UI 的默认监听地址。
func LANAddrs(ifname string) ([]netip.Addr, error) {
	prefixes, err := interfacePrefixes(ifname)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, len(prefixes))
	for i, p := range prefixes {
		out[i] = p.Addr()
	}
	return out, nil
}

// LANPrefixes 返回网络接口上配置的全部 IPv4 网段，用作默认的受管网段。
func LANPrefixes(ifname string) ([]netip.Prefix, error) {
	prefixes, err := interfacePrefixes(ifname)
	if err != nil {
		return nil, err
	}
	for i, p := range prefixes {
		prefixes[i] = p.Masked()
	}
	return prefixes, nil
}

func interfacePrefixes(ifname string) ([]netip.Prefix, error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifname, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifname, err)
	}
	var out []netip.Prefix
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.Is4() {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		out = append(out, netip.PrefixFrom(ip, ones))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("interface %s has no IPv4 address", ifname)
	}
	return out, nil
}
