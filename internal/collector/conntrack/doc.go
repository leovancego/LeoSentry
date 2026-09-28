// Package conntrack 周期性快照内核连接跟踪表并与上次差分，
// 得到受管设备的实时在线/活跃状态（仅内存，不进历史记录）。
// 优先使用 ctnetlink，未安装 kmod-nf-conntrack-netlink 时回退到 /proc/net/nf_conntrack。
package conntrack
