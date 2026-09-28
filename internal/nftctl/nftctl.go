package nftctl

import (
	"net/netip"
	"time"
)

// LeoSentry 自有 nft 表中的对象名。
const (
	TableName         = "leosentry"
	SetManagedDevices = "managed_devices"
	SetTrafficUp      = "traffic_up"
	SetTrafficDown    = "traffic_down"
	// 以下集合只通过增删元素更新，规则在 Setup 时安装一次。
	SetBlockAll    = "block_all"
	SetBlockAllMAC = "block_all_mac"
	SetBlockVideo  = "block_video"
	SetBlockGame   = "block_game"
	SetGameDst     = "game_dst"
	SetVideoDst    = "video_dst"
	ChainForward   = "forward"
	// ForwardPriority 早于 fw4 的 forward 链（priority 0），保证在 fw4 做出放行/拒绝判定前完成计数。
	ForwardPriority = -10
)

// Options 配置采集用 nft 表。
type Options struct {
	// ManagedNetworks 写入 managed_devices 集合，只统计这些网段内设备的流量。
	ManagedNetworks []netip.Prefix
	// TrafficTimeout 是流量集合元素的空闲超时，元素被 update 命中时刷新。
	TrafficTimeout time.Duration
	// TrafficSetSize 是流量集合的元素上限，满了之后新组合不再计数（不影响转发）。
	TrafficSetSize uint32
}

// DefaultOptions 返回默认的集合参数。
func DefaultOptions(managed []netip.Prefix) Options {
	return Options{
		ManagedNetworks: managed,
		TrafficTimeout:  time.Hour,
		TrafficSetSize:  65535,
	}
}
