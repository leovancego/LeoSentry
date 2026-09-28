//go:build linux

package nftstats

import (
	"fmt"

	"github.com/google/nftables"

	"github.com/leo/leosentry/internal/nftctl"
)

// Reader 通过 netlink 读取 traffic_up / traffic_down 集合元素上的计数器。
type Reader struct {
	conn     *nftables.Conn
	up, down *nftables.Set
	upDelta  deltaTracker
	dnDelta  deltaTracker
}

// NewReader 连接 nft 并定位流量集合，需在 nftctl.Controller.Setup 之后调用。
func NewReader() (*Reader, error) {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return nil, fmt.Errorf("nftstats: %w", err)
	}
	table := &nftables.Table{Name: nftctl.TableName, Family: nftables.TableFamilyINet}
	up, err := conn.GetSetByName(table, nftctl.SetTrafficUp)
	if err != nil {
		conn.CloseLasting()
		return nil, fmt.Errorf("nftstats: get set %s: %w", nftctl.SetTrafficUp, err)
	}
	down, err := conn.GetSetByName(table, nftctl.SetTrafficDown)
	if err != nil {
		conn.CloseLasting()
		return nil, fmt.Errorf("nftstats: get set %s: %w", nftctl.SetTrafficDown, err)
	}
	return &Reader{conn: conn, up: up, down: down, upDelta: newDeltaTracker(), dnDelta: newDeltaTracker()}, nil
}

// Read 读取两个集合并返回本周期内有流量增量的 FlowPair。
func (r *Reader) Read() (map[FlowPair]Traffic, error) {
	upCur, err := r.readSet(r.up)
	if err != nil {
		return nil, err
	}
	downCur, err := r.readSet(r.down)
	if err != nil {
		return nil, err
	}

	out := make(map[FlowPair]Traffic, len(upCur))
	r.upDelta.update(upCur, func(k FlowPair, d uint64) {
		t := out[k]
		t.BytesUp += d
		out[k] = t
	})
	r.dnDelta.update(downCur, func(k FlowPair, d uint64) {
		t := out[k]
		t.BytesDown += d
		out[k] = t
	})
	return out, nil
}

// Close 关闭 netlink 连接。
func (r *Reader) Close() error {
	return r.conn.CloseLasting()
}

func (r *Reader) readSet(s *nftables.Set) (map[FlowPair]uint64, error) {
	elems, err := r.conn.GetSetElements(s)
	if err != nil {
		return nil, fmt.Errorf("nftstats: read set %s: %w", s.Name, err)
	}
	out := make(map[FlowPair]uint64, len(elems))
	for _, e := range elems {
		if e.Counter == nil {
			continue
		}
		if k, ok := decodeKey(e.Key); ok {
			out[k] = e.Counter.Bytes
		}
	}
	return out, nil
}
