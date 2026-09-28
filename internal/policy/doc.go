// Package policy 是策略引擎：按设备评估暂停、临时延长、每日时长和娱乐时段，
// 得到期望的控制状态，再交给 nftctl 做差量下发。集合没变化时不写 nftables。
package policy
