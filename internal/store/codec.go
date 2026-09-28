package store

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
)

// 库中 MAC 与 IPv4 地址以整数存储：MAC 为 48 位整数，IPv4 为网络字节序的 32 位整数。
// 相比文本，单行与两个索引的体积都显著减小。可读形式见视图 v_usage_records。

// encodeMAC 把 "aa:bb:cc:dd:ee:ff" 编码为整数；空串或无法解析时为 0（未知设备）。
func encodeMAC(s string) int64 {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return 0
	}
	var b [8]byte
	copy(b[2:], hw)
	return int64(binary.BigEndian.Uint64(b[:]))
}

// decodeMAC 是 encodeMAC 的逆运算，0 还原为空串。
func decodeMAC(v int64) string {
	if v == 0 {
		return ""
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[2], b[3], b[4], b[5], b[6], b[7])
}

// encodeIPv4 把 IPv4 地址编码为整数；非 IPv4 地址为 0。
func encodeIPv4(a netip.Addr) int64 {
	if !a.Is4() {
		return 0
	}
	b := a.As4()
	return int64(binary.BigEndian.Uint32(b[:]))
}

// decodeIPv4 是 encodeIPv4 的逆运算。
func decodeIPv4(v int64) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return netip.AddrFrom4(b)
}
