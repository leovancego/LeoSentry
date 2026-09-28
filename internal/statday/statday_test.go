package statday

import (
	"testing"
	"time"
)

func TestDayStart(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	c := New(3, 0, loc)
	cases := []struct {
		now, want string
	}{
		{"2026-09-26 14:00", "2026-09-26 03:00"},
		{"2026-09-26 03:00", "2026-09-26 03:00"},
		{"2026-09-26 02:59", "2026-09-25 03:00"},
		{"2026-10-01 00:30", "2026-09-30 03:00"},
	}
	for _, tc := range cases {
		now, _ := time.ParseInLocation("2006-01-02 15:04", tc.now, loc)
		want, _ := time.ParseInLocation("2006-01-02 15:04", tc.want, loc)
		if got := c.DayStart(now); !got.Equal(want) {
			t.Errorf("DayStart(%s) = %s, want %s", tc.now, got, want)
		}
	}
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-09-26 02:00", loc)
	if got := c.Label(c.DayStart(now)); got != "2026-09-25" {
		t.Errorf("Label = %s", got)
	}
	if got := c.NextStart(now); got.Format("2006-01-02 15:04") != "2026-09-26 03:00" {
		t.Errorf("NextStart = %s", got)
	}
}
