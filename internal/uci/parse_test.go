package uci

import (
	"strings"
	"testing"
)

const sample = `
config dnsmasq
	option domainneeded '1'
	option logqueries "1"   # trailing comment

config host 'nas'
	option name 'nas'
	option mac 'AA:BB:CC:DD:EE:01 aa:bb:cc:dd:ee:02'
	option ip '192.168.1.5'

config host
	option name 'it'\''s'
	list tag a
	list tag 'b c'
`

func TestParse(t *testing.T) {
	f, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(f.Sections); got != 3 {
		t.Fatalf("sections = %d, want 3", got)
	}
	if v := f.ByType("dnsmasq")[0].Get("logqueries"); v != "1" {
		t.Errorf("logqueries = %q", v)
	}
	nas := f.Named("nas")
	if nas == nil || nas.Get("ip") != "192.168.1.5" || nas.Index != 0 {
		t.Fatalf("nas section = %+v", nas)
	}
	hosts := f.ByType("host")
	if hosts[1].Index != 1 || hosts[1].Get("name") != "it's" {
		t.Errorf("second host = %+v", hosts[1])
	}
	if tags := hosts[1].List("tag"); len(tags) != 2 || tags[1] != "b c" {
		t.Errorf("tags = %q", tags)
	}
}

func TestParseUnterminatedQuote(t *testing.T) {
	if _, err := Parse(strings.NewReader("config a\n\toption x 'oops\n")); err == nil {
		t.Fatal("expected error")
	}
}
