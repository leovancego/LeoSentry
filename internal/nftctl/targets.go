package nftctl

import (
	"net/netip"
	"slices"
)

// Targets 是期望写入 inet leosentry 的设备与目标地址。
// 同一台设备只会出现在一个拦截集合里：断网、禁娱乐、禁游戏三者取最严的那个。
type Targets struct {
	BlockAll    []netip.Addr
	BlockAllMAC []string
	BlockVideo  []netip.Addr
	BlockGame   []netip.Addr
	GameDest    []netip.Addr
	VideoDest   []netip.Addr
}

// SetOp 是某个集合相对上次成功下发需要增删的元素。
// IP 集合用 Add/Del，网卡地址集合用 AddMAC/DelMAC。
type SetOp struct {
	Name   string
	Add    []netip.Addr
	Del    []netip.Addr
	AddMAC []string
	DelMAC []string
}

// Canon 去掉无效地址、去重并按地址排序，便于比较。
func Canon(t Targets) Targets {
	return Targets{
		BlockAll:    canon(t.BlockAll),
		BlockAllMAC: canonMAC(t.BlockAllMAC),
		BlockVideo:  canon(t.BlockVideo),
		BlockGame:   canon(t.BlockGame),
		GameDest:    canon(t.GameDest),
		VideoDest:   canon(t.VideoDest),
	}
}

// Diff 比较两次期望。没有任何元素变化时 changed 为 false，调用方不应写 nftables。
func Diff(prev, next Targets) ([]SetOp, bool) {
	prev, next = Canon(prev), Canon(next)
	var ops []SetOp
	for _, pair := range []struct {
		name string
		old  []netip.Addr
		new  []netip.Addr
	}{
		{SetBlockAll, prev.BlockAll, next.BlockAll},
		{SetBlockVideo, prev.BlockVideo, next.BlockVideo},
		{SetBlockGame, prev.BlockGame, next.BlockGame},
		{SetGameDst, prev.GameDest, next.GameDest},
		{SetVideoDst, prev.VideoDest, next.VideoDest},
	} {
		add, del := delta(pair.old, pair.new)
		if len(add) == 0 && len(del) == 0 {
			continue
		}
		ops = append(ops, SetOp{Name: pair.name, Add: add, Del: del})
	}
	if add, del := deltaMAC(prev.BlockAllMAC, next.BlockAllMAC); len(add) > 0 || len(del) > 0 {
		ops = append(ops, SetOp{Name: SetBlockAllMAC, AddMAC: add, DelMAC: del})
	}
	return ops, len(ops) > 0
}

func canon(ips []netip.Addr) []netip.Addr {
	if len(ips) == 0 {
		return nil
	}
	seen := make(map[netip.Addr]struct{}, len(ips))
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if !ip.IsValid() || !ip.Is4() {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}

func canonMAC(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, m := range in {
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

func deltaMAC(prev, next []string) (add, del []string) {
	i, j := 0, 0
	for i < len(prev) && j < len(next) {
		switch {
		case prev[i] == next[j]:
			i++
			j++
		case prev[i] < next[j]:
			del = append(del, prev[i])
			i++
		default:
			add = append(add, next[j])
			j++
		}
	}
	del = append(del, prev[i:]...)
	add = append(add, next[j:]...)
	return add, del
}

func delta(prev, next []netip.Addr) (add, del []netip.Addr) {
	i, j := 0, 0
	for i < len(prev) && j < len(next) {
		switch prev[i].Compare(next[j]) {
		case 0:
			i++
			j++
		case -1:
			del = append(del, prev[i])
			i++
		default:
			add = append(add, next[j])
			j++
		}
	}
	del = append(del, prev[i:]...)
	add = append(add, next[j:]...)
	return add, del
}
