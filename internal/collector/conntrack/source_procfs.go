package conntrack

import (
	"bufio"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// procfsSource 解析 /proc/net/nf_conntrack，作为未安装 kmod-nf-conntrack-netlink 时的兜底来源。
type procfsSource struct {
	path string
}

// NewProcfsSource 创建基于 procfs 的来源，文件不存在时返回错误。
func NewProcfsSource(path string) (Source, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return &procfsSource{path: path}, nil
}

func (s *procfsSource) Name() string { return "procfs" }
func (s *procfsSource) Close() error { return nil }

func (s *procfsSource) Snapshot(dst map[flowKey]struct{}, keep func([4]byte) bool) error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		if k, ok := parseProcLine(sc.Text()); ok && keep(k.src) {
			dst[k] = struct{}{}
		}
	}
	return sc.Err()
}

// parseProcLine 解析一行 nf_conntrack，只取原方向（第一组）src/dst/sport/dport：
//
//	ipv4 2 tcp 6 7440 ESTABLISHED src=192.168.1.10 dst=1.2.3.4 sport=51234 dport=443 ... src=1.2.3.4 dst=...
func parseProcLine(line string) (flowKey, bool) {
	if !strings.HasPrefix(line, "ipv4") {
		return flowKey{}, false
	}
	f := strings.Fields(line)
	if len(f) < 6 {
		return flowKey{}, false
	}
	proto, err := strconv.ParseUint(f[3], 10, 8)
	if err != nil {
		return flowKey{}, false
	}
	k := flowKey{proto: uint8(proto)}
	var haveSrc, haveDst, haveSport, haveDport bool
	for _, field := range f[4:] {
		key, val, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "src":
			if haveSrc {
				return k, haveDst
			}
			a, err := netip.ParseAddr(val)
			if err != nil || !a.Is4() {
				return flowKey{}, false
			}
			k.src, haveSrc = a.As4(), true
		case "dst":
			if haveDst {
				continue
			}
			a, err := netip.ParseAddr(val)
			if err != nil || !a.Is4() {
				return flowKey{}, false
			}
			k.dst, haveDst = a.As4(), true
		case "sport":
			if !haveSport {
				n, _ := strconv.ParseUint(val, 10, 16)
				k.sport, haveSport = uint16(n), true
			}
		case "dport":
			if !haveDport {
				n, _ := strconv.ParseUint(val, 10, 16)
				k.dport, haveDport = uint16(n), true
			}
		}
	}
	return k, haveSrc && haveDst
}
