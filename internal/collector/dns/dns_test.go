package dns

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stamp(t time.Time) string {
	return t.Format("Jan _2 15:04:05")
}

func TestParseLine(t *testing.T) {
	now := time.Now()
	loc := time.Local
	cases := []struct {
		line   string
		ok     bool
		kind   lineKind
		serial uint64
		domain string
		answer string
	}{
		{"Sep 26 14:00:00 dnsmasq[1234]: 5 192.168.1.10/53712 query[A] Example.COM from 192.168.1.10", true, kindQuery, 5, "example.com", ""},
		{"Sep  6 14:00:00 dnsmasq[1234]: 5 192.168.1.10/53712 reply example.com is 93.184.216.34", true, kindAnswer, 5, "example.com", "93.184.216.34"},
		{"Sep 26 14:00:00 dnsmasq[1234]: cached example.com is 93.184.216.34", true, kindAnswer, 0, "example.com", "93.184.216.34"},
		{"Sep 26 14:00:00 dnsmasq[1234]: /tmp/hosts/dhcp.cfg01411c nas.lan is 192.168.1.5", true, kindAnswer, 0, "nas.lan", "192.168.1.5"},
		{"Sep 26 14:00:00 dnsmasq[1234]: 5 192.168.1.10/53712 forwarded example.com to 8.8.8.8", false, 0, 0, "", ""},
		{"Sep 26 14:00:00 dnsmasq[1234]: 6 192.168.1.10/53712 query[HTTPS] example.com from 192.168.1.10", false, 0, 0, "", ""},
		{"Sep 26 14:00:00 dnsmasq-dhcp[1234]: DHCPACK(br-lan) 192.168.1.10", false, 0, 0, "", ""},
	}
	for _, tc := range cases {
		l, ok := parseLine(tc.line, loc, now)
		if ok != tc.ok {
			t.Errorf("%q ok=%v", tc.line, ok)
			continue
		}
		if !ok {
			continue
		}
		if l.Kind != tc.kind || l.Serial != tc.serial || l.Domain != tc.domain || l.Answer != tc.answer {
			t.Errorf("%q => %+v", tc.line, l)
		}
	}
}

func TestMapSerialAndCNAME(t *testing.T) {
	now := time.Now()
	ts := stamp(now)
	log := []string{
		// extra 模式：CNAME 链末端的 IP 归属到最初查询的域名
		ts + " dnsmasq[1]: 7 192.168.1.10/1000 query[A] www.game.com from 192.168.1.10",
		ts + " dnsmasq[1]: 7 192.168.1.10/1000 reply www.game.com is <CNAME>",
		ts + " dnsmasq[1]: 7 192.168.1.10/1000 reply edge.cdn.net is 1.1.1.1",
		// 非 extra 模式
		ts + " dnsmasq[1]: query[A] video.com from 192.168.1.11",
		ts + " dnsmasq[1]: reply video.com is <CNAME>",
		ts + " dnsmasq[1]: reply v.cdn.net is 2.2.2.2",
		ts + " dnsmasq[1]: reply v.cdn.net is 2.2.2.3",
		ts + " dnsmasq[1]: query[A] plain.com from 192.168.1.11",
		ts + " dnsmasq[1]: reply plain.com is 3.3.3.3",
	}
	m := NewMap(30 * time.Minute)
	var lines []logLine
	for _, s := range log {
		if l, ok := parseLine(s, time.Local, now); ok {
			lines = append(lines, l)
		}
	}
	m.ingest(lines, now)

	want := map[string]string{
		"1.1.1.1": "www.game.com",
		"2.2.2.2": "video.com",
		"2.2.2.3": "video.com",
		"3.3.3.3": "plain.com",
		"9.9.9.9": "",
	}
	for ip, domain := range want {
		if got := m.Lookup(netip.MustParseAddr(ip)); got != domain {
			t.Errorf("Lookup(%s) = %q, want %q", ip, got, domain)
		}
	}
}

func TestMapExpiry(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	m := NewMap(30 * time.Minute)
	l, _ := parseLine(stamp(old)+" dnsmasq[1]: reply old.com is 4.4.4.4", time.Local, now)
	m.ingest([]logLine{l}, now)
	if got := m.Lookup(netip.MustParseAddr("4.4.4.4")); got != "" {
		t.Errorf("expired entry returned %q", got)
	}
	m.Sweep(now)
	if m.Len() != 0 {
		t.Errorf("Len = %d after sweep", m.Len())
	}
}

func TestTailerIncrementalAndTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnsmasq_query.log")
	ts := stamp(time.Now())
	write := func(s string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	m := NewMap(30 * time.Minute)
	tl := NewTailer(m, TailerOptions{
		Path:    path,
		MaxSize: 1 << 20,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	defer tl.close()

	write(ts + " dnsmasq[1]: reply a.com is 5.5.5.5\n" + ts + " dnsmasq[1]: reply b.c")
	tl.poll()
	if m.Lookup(netip.MustParseAddr("5.5.5.5")) != "a.com" {
		t.Fatal("first line not ingested")
	}
	write("om is 6.6.6.6\n")
	tl.poll()
	if got := m.Lookup(netip.MustParseAddr("6.6.6.6")); got != "b.com" {
		t.Fatalf("partial line join failed: %q", got)
	}

	tl.opts.MaxSize = 1
	write(ts + " dnsmasq[1]: reply c.com is 7.7.7.7\n")
	tl.poll()
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Fatalf("log not truncated, size=%d", st.Size())
	}
	write(fmt.Sprintf("%s dnsmasq[1]: reply d.com is 8.8.8.8\n", ts))
	tl.poll()
	if got := m.Lookup(netip.MustParseAddr("8.8.8.8")); got != "d.com" {
		t.Fatalf("after truncate: %q", got)
	}
	if !strings.Contains(m.Lookup(netip.MustParseAddr("7.7.7.7")), "c.com") {
		t.Fatal("line before truncate lost")
	}
}
