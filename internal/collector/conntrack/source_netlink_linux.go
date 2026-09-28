//go:build linux

package conntrack

import (
	ct "github.com/ti-mo/conntrack"
)

// netlinkSource 通过 ctnetlink dump 连接跟踪表，内核会按需自动加载 nf_conntrack_netlink 模块。
type netlinkSource struct {
	conn *ct.Conn
}

// NewNetlinkSource 建立 ctnetlink 连接并试探一次 dump，失败说明模块不可用。
func NewNetlinkSource() (Source, error) {
	conn, err := ct.Dial(nil)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Dump(nil); err != nil {
		conn.Close()
		return nil, err
	}
	return &netlinkSource{conn: conn}, nil
}

func (s *netlinkSource) Name() string { return "netlink" }
func (s *netlinkSource) Close() error { return s.conn.Close() }

func (s *netlinkSource) Snapshot(dst map[flowKey]struct{}, keep func([4]byte) bool) error {
	flows, err := s.conn.Dump(nil)
	if err != nil {
		return err
	}
	for i := range flows {
		t := &flows[i].TupleOrig
		src, dstAddr := t.IP.SourceAddress, t.IP.DestinationAddress
		if !src.Is4() || !dstAddr.Is4() {
			continue
		}
		k := flowKey{
			proto: t.Proto.Protocol,
			src:   src.As4(),
			dst:   dstAddr.As4(),
			sport: t.Proto.SourcePort,
			dport: t.Proto.DestinationPort,
		}
		if keep(k.src) {
			dst[k] = struct{}{}
		}
	}
	return nil
}
