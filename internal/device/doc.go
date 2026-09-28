// Package device 负责局域网设备发现与识别：读取 DHCP 租约、
// 内核邻居表（ARP/NDP），维护 MAC ↔ IPv4 ↔ 主机名 映射，
// 并与用户在 Web 上设定的设备别名、历史地址对照。
package device
