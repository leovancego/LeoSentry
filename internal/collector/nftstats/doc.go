// Package nftstats 通过 netlink 读取 inet leosentry 表中 traffic_up / traffic_down
// 集合元素上的计数器，与上次读数相减得到每个 (设备IP, 目标IP) 的本周期上下行增量。
// 这是历史记录的唯一数据源。
package nftstats
