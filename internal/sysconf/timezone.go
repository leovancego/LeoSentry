package sysconf

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/uci"
)

// OpenWrt 不提供 /etc/localtime，Go 运行时默认会退化为 UTC，
// 因此需要从系统配置中自行解析时区。
const (
	systemConfigFile = "/etc/config/system"
	tzFile           = "/etc/TZ"
)

// DetectLocation 按以下顺序确定时区：
//  1. 显式配置的 IANA 时区名；
//  2. system.@system[0].zonename（如 Asia/Shanghai，依赖内嵌的 time/tzdata）；
//  3. /etc/TZ 中的 POSIX 时区串（如 CST-8），仅取其固定偏移。
func DetectLocation(explicit string) (*time.Location, error) {
	if explicit != "" {
		return time.LoadLocation(explicit)
	}
	if f, err := uci.ParseFile(systemConfigFile); err == nil {
		if s := f.ByType("system"); len(s) > 0 {
			if name := s[0].Get("zonename"); name != "" {
				if loc, err := time.LoadLocation(strings.ReplaceAll(name, " ", "_")); err == nil {
					return loc, nil
				}
			}
		}
	}
	if data, err := os.ReadFile(tzFile); err == nil {
		return parsePOSIXTZ(strings.TrimSpace(string(data)))
	}
	return time.Local, nil
}

// parsePOSIXTZ 解析 POSIX TZ 串的标准时间部分，例如 "CST-8"、"<+0530>-5:30"。
// POSIX 偏移的符号与 UTC 偏移相反：CST-8 表示 UTC+8。夏令时规则被忽略。
func parsePOSIXTZ(s string) (*time.Location, error) {
	var name string
	switch {
	case strings.HasPrefix(s, "<"):
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return nil, fmt.Errorf("invalid TZ %q", s)
		}
		name, s = s[1:end], s[end+1:]
	default:
		i := strings.IndexFunc(s, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') })
		if i <= 0 {
			return nil, fmt.Errorf("invalid TZ %q", s)
		}
		name, s = s[:i], s[i:]
	}

	sign := 1
	switch {
	case strings.HasPrefix(s, "-"):
		sign, s = -1, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	end := strings.IndexFunc(s, func(r rune) bool { return !(r >= '0' && r <= '9' || r == ':') })
	if end < 0 {
		end = len(s)
	}
	parts := strings.Split(s[:end], ":")
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid TZ offset %q", s)
	}
	secs := hours * 3600
	if len(parts) > 1 {
		m, _ := strconv.Atoi(parts[1])
		secs += m * 60
	}
	return time.FixedZone(name, -sign*secs), nil
}
