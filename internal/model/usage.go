package model

import "net/netip"

// UsageEvent 描述一个采集周期内某设备与某目标之间的流量增量，
// 对应 today.db 中 usage_records 表的一行。
type UsageEvent struct {
	// CollectedAt 是采集周期的统一时间戳（Unix 秒），同一周期内所有记录共用。
	CollectedAt int64
	// DeviceMAC 是设备的稳定标识（小写冒号分隔），无法换算时为空串。
	DeviceMAC string
	// DeviceIP 是记录时刻的设备地址，仅作附加信息。
	DeviceIP netip.Addr
	TargetIP netip.Addr
	// Domain 由 DNS Map 反查得到，查不到时为空串。
	Domain string
	// BytesUp 是本周期内设备→目标的字节增量。
	BytesUp int64
	// BytesDown 是本周期内目标→设备的字节增量。
	BytesDown int64
}

// UsageBatch 是一个采集周期产出的全部记录。
type UsageBatch struct {
	CollectedAt int64
	Events      []UsageEvent
}

// UsageTotal 是某设备（及某域名）在统计日内的累计值。
type UsageTotal struct {
	DeviceMAC string
	// Domain 在按设备汇总时为空，在按设备+域名汇总时为域名（未知域名为空串）。
	Domain    string
	BytesUp   int64
	BytesDown int64
	// ActivePeriods 是有流量的采集周期数。
	ActivePeriods int64
}
