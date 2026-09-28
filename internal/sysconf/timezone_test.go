package sysconf

import (
	"testing"
	"time"
)

func timeZoneOf(loc *time.Location) (string, int) {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
}

func TestParsePOSIXTZ(t *testing.T) {
	cases := []struct {
		in     string
		name   string
		offset int
	}{
		{"CST-8", "CST", 8 * 3600},
		{"UTC0", "UTC", 0},
		{"EST5EDT,M3.2.0,M11.1.0", "EST", -5 * 3600},
		{"<+0530>-5:30", "+0530", 5*3600 + 30*60},
	}
	for _, tc := range cases {
		loc, err := parsePOSIXTZ(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		name, off := timeZoneOf(loc)
		if name != tc.name || off != tc.offset {
			t.Errorf("%s = %s %d, want %s %d", tc.in, name, off, tc.name, tc.offset)
		}
	}
}
