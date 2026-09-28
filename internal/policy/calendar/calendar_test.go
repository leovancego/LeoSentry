package calendar

import (
	"testing"
	"time"

	"github.com/leo/leosentry/assets"
)

func TestOfficial2026(t *testing.T) {
	raw, err := assets.Calendar.ReadFile("calendar/cn-2026.json")
	if err != nil {
		t.Fatal(err)
	}
	y, vac, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !y.Official || y.Meta.Year != 2026 || y.Meta.Document != "国办发明电〔2025〕7号" {
		t.Fatalf("meta = %+v", y.Meta)
	}
	if len(y.Days) != 365 {
		t.Fatalf("days = %d", len(y.Days))
	}
	if vac == nil || vac.Summer.Start != "2026-07-01" || vac.Winter.End != "2026-02-28" {
		t.Fatalf("vacations = %+v", vac)
	}

	// 日期、星期、是否工作日、名称、种类。星期按国务院通知核对。
	want := []struct {
		date, name, kind string
		weekday          int
		work             bool
	}{
		{"2026-01-01", "元旦", "off", 4, false},
		{"2026-01-04", "元旦调休", "makeup", 7, true},
		{"2026-01-05", "", "", 1, true},
		{"2026-02-14", "春节调休", "makeup", 6, true},
		{"2026-02-15", "春节", "off", 7, false},
		{"2026-02-23", "春节", "off", 1, false},
		{"2026-02-28", "春节调休", "makeup", 6, true},
		{"2026-03-01", "", "", 7, false},
		{"2026-03-02", "", "", 1, true},
		{"2026-04-04", "清明节", "off", 6, false},
		{"2026-04-06", "清明节", "off", 1, false},
		{"2026-05-01", "劳动节", "off", 5, false},
		{"2026-05-05", "劳动节", "off", 2, false},
		{"2026-05-09", "劳动节调休", "makeup", 6, true},
		{"2026-06-19", "端午节", "off", 5, false},
		{"2026-09-20", "国庆节调休", "makeup", 7, true},
		{"2026-09-25", "中秋节", "off", 5, false},
		{"2026-09-26", "中秋节", "off", 6, false},
		{"2026-09-27", "中秋节", "off", 7, false},
		{"2026-10-01", "国庆节", "off", 4, false},
		{"2026-10-07", "国庆节", "off", 3, false},
		{"2026-10-08", "", "", 4, true},
		{"2026-10-10", "国庆节调休", "makeup", 6, true},
	}
	off, makeup := 0, 0
	for _, d := range y.Days {
		switch d.Kind {
		case "off":
			off++
		case "makeup":
			makeup++
		}
		got, ok := y.Get(d.Date)
		if !ok || got != d {
			t.Fatalf("index miss %s", d.Date)
		}
	}
	if off != 33 || makeup != 6 {
		t.Fatalf("off = %d, makeup = %d", off, makeup)
	}
	for _, w := range want {
		d, ok := y.Get(w.date)
		if !ok {
			t.Fatalf("missing %s", w.date)
		}
		wd := isoWeekday(mustDate(t, w.date))
		if wd != w.weekday || d.Weekday != w.weekday || d.Workday != w.work || d.Name != w.name || d.Kind != w.kind {
			t.Fatalf("%s got weekday %d work %v name %q kind %q, want weekday %d work %v name %q kind %q",
				w.date, d.Weekday, d.Workday, d.Name, d.Kind, w.weekday, w.work, w.name, w.kind)
		}
	}
}

func TestRejectsBadCalendar(t *testing.T) {
	_, _, err := Parse([]byte(`{"version":1,"region":"CN","year":2026,"source":"x","holidays":[{"name":"元旦","off":["2026-01-01"],"work":["2026-01-01"]}]}`))
	if err == nil {
		t.Fatal("duplicate off/work accepted")
	}
	_, _, err = Parse([]byte(`{"version":1,"region":"CN","year":2026,"source":"x","holidays":[{"name":"元旦","off":["2026-02-31"]}]}`))
	if err == nil {
		t.Fatal("invalid date accepted")
	}
	if err := (Vacations{Summer: Span{Start: "2026-07-01", End: "2026-08-01"}, Winter: Span{Start: "2026-08-01", End: "2026-08-20"}}).Validate(); err == nil {
		t.Fatal("overlapping vacations accepted")
	}
}

func TestTemplateRoundTrip(t *testing.T) {
	b, name, err := Template(2026)
	if err != nil || name != "cn-2026.json" || len(b) == 0 {
		t.Fatalf("template: %v %s", err, name)
	}
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
