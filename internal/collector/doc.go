// Package collector 是采集层的主循环：按固定周期读取 nft 流量计数增量，
// 结合设备对照表与 DNS Map 组装 UsageEvent，交给 today.db 与内存当日计数器。
// 采集器只读内核状态，不处理任何网络包，也不做分类或策略判断。
package collector
