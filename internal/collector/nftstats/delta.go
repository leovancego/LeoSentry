package nftstats

import "net/netip"

// FlowPair 是流量集合的键：设备IP . 目标IP。
type FlowPair struct {
	Device [4]byte
	Target [4]byte
}

// DeviceAddr 返回设备地址。
func (p FlowPair) DeviceAddr() netip.Addr { return netip.AddrFrom4(p.Device) }

// TargetAddr 返回目标地址。
func (p FlowPair) TargetAddr() netip.Addr { return netip.AddrFrom4(p.Target) }

// Traffic 是一个采集周期内某 FlowPair 的上下行字节增量。
type Traffic struct {
	BytesUp   uint64
	BytesDown uint64
}

// deltaTracker 保存上次读到的计数器绝对值，把累计值换算为本周期增量。
//
//   - 新出现的元素：表在启动时重建、计数从零开始，且元素只可能在上次读取之后创建，
//     因此当前值就是增量；
//   - 当前值小于上次值：元素超时被删除后又重新创建，同样把当前值当作增量；
//   - 本次未出现的元素：已超时删除，从记录中移除。
type deltaTracker struct {
	last map[FlowPair]uint64
}

func newDeltaTracker() deltaTracker {
	return deltaTracker{last: make(map[FlowPair]uint64)}
}

// update 用本次读数替换上次读数，并对每个增量非零的键调用 emit。
func (d *deltaTracker) update(cur map[FlowPair]uint64, emit func(FlowPair, uint64)) {
	for k, v := range cur {
		prev, ok := d.last[k]
		var delta uint64
		switch {
		case !ok || v < prev:
			delta = v
		default:
			delta = v - prev
		}
		if delta > 0 {
			emit(k, delta)
		}
	}
	d.last = cur
}

// decodeKey 解析拼接键：两个 IPv4 地址各占 4 字节。
func decodeKey(key []byte) (FlowPair, bool) {
	if len(key) != 8 {
		return FlowPair{}, false
	}
	var p FlowPair
	copy(p.Device[:], key[:4])
	copy(p.Target[:], key[4:])
	return p, true
}
