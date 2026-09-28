package dns

import (
	"strconv"
	"strings"
	"time"
)

type lineKind uint8

const (
	kindQuery lineKind = iota + 1
	kindAnswer
)

// logLine 是一行 dnsmasq 查询日志中与 DNS Map 相关的信息。
type logLine struct {
	Time time.Time
	// Serial 是 log-queries=extra 模式下的查询流水号，同一次查询的 query/reply 行共用。
	Serial    uint64
	HasSerial bool
	Kind      lineKind
	Domain    string
	// Answer 是应答内容：IP 地址或 <CNAME>、NXDOMAIN 等标记。
	Answer string
}

// parseLine 解析 dnsmasq 写入 log-facility 文件的一行日志：
//
//	Sep 26 14:00:00 dnsmasq[1234]: 5 192.168.1.10/53712 query[A] example.com from 192.168.1.10
//	Sep 26 14:00:00 dnsmasq[1234]: 5 192.168.1.10/53712 reply example.com is 93.184.216.34
//	Sep 26 14:00:00 dnsmasq[1234]: cached example.com is 93.184.216.34
//
// 第三种为未开启 extra 时的格式（无流水号与客户端）。与 DNS Map 无关的行返回 ok=false。
func parseLine(line string, loc *time.Location, now time.Time) (logLine, bool) {
	f := strings.Fields(line)
	if len(f) < 6 || !strings.HasPrefix(f[3], "dnsmasq") {
		return logLine{}, false
	}
	l := logLine{Time: parseSyslogTime(f[0], f[1], f[2], loc, now)}
	msg := f[4:]

	if len(msg) >= 3 && strings.IndexByte(msg[1], '/') > 0 {
		if n, err := strconv.ParseUint(msg[0], 10, 64); err == nil {
			l.Serial, l.HasSerial = n, true
			msg = msg[2:]
		}
	}

	switch verb := msg[0]; {
	case strings.HasPrefix(verb, "query["):
		if verb != "query[A]" && verb != "query[AAAA]" || len(msg) < 2 {
			return logLine{}, false
		}
		l.Kind = kindQuery
		l.Domain = strings.ToLower(msg[1])
	case verb == "reply" || verb == "cached" || verb == "cached-stale" || verb == "config" || strings.HasPrefix(verb, "/"):
		if len(msg) < 4 || msg[2] != "is" {
			return logLine{}, false
		}
		l.Kind = kindAnswer
		l.Domain = strings.ToLower(msg[1])
		l.Answer = msg[3]
	default:
		return logLine{}, false
	}
	return l, true
}

// parseSyslogTime 解析不含年份的 syslog 时间（"Sep 26 14:00:00"）。
// 年份取当前年，若结果比 now 晚一天以上则视为去年的日志。解析失败返回零值。
func parseSyslogTime(month, day, clock string, loc *time.Location, now time.Time) time.Time {
	t, err := time.ParseInLocation("Jan 2 15:04:05 2006", month+" "+day+" "+clock+" "+strconv.Itoa(now.Year()), loc)
	if err != nil {
		return time.Time{}
	}
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t
}
