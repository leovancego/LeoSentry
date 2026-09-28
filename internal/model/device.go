package model

import (
	"net/netip"
	"time"
)

// DeviceSource 标识设备 IP→MAC 对应关系的来源。
type DeviceSource uint8

const (
	SourceLease DeviceSource = iota + 1
	SourceStatic
	SourceNeighbor
)

func (s DeviceSource) String() string {
	switch s {
	case SourceLease:
		return "lease"
	case SourceStatic:
		return "static"
	case SourceNeighbor:
		return "neighbor"
	default:
		return "unknown"
	}
}

// Device 是局域网中的一台设备在当前时刻的身份信息。
type Device struct {
	MAC      string
	IP       netip.Addr
	Hostname string
	Source   DeviceSource
}

// KnownDevice 是一台设备的持久身份：用户命名、首次出现、历史 IP。
// 按 MAC 识别，不随 DHCP 换地址而改变。
type KnownDevice struct {
	MAC       string
	Name      string
	FirstSeen int64
	LastSeen  int64
	LastIP    netip.Addr
	IPs       []KnownIP
}

// KnownIP 是某台设备历史上用过的一个地址。
type KnownIP struct {
	IP        netip.Addr
	FirstSeen int64
	LastSeen  int64
}

// DeviceActivity 是基于 conntrack 快照差分得到的设备瞬时在线/活跃状态。
type DeviceActivity struct {
	IP       netip.Addr
	MAC      string
	Hostname string
	// Online 表示设备当前存在未结束的连接。
	Online bool
	// Flows 是当前连接数，NewFlows / EndedFlows 是本周期新建与结束的连接数。
	Flows        int
	NewFlows     int
	EndedFlows   int
	LastActiveAt time.Time
	UpdatedAt    time.Time
}
