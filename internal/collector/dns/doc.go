// Package dns 维护内存中的 DNS Map（目标IP → 域名，带有效期）。
// 数据源是 dnsmasq 查询日志（log-queries=extra 输出到 log-facility 文件）：
// 启动时读取已有日志预热，运行时增量跟踪，按流水号把 query 与 reply 配对，
// 条目 30 分钟未刷新即过期。该 Map 用于把流量记录"翻译"成可读域名，
// 也是后续判断"是否在玩游戏"的关键依据。
package dns
