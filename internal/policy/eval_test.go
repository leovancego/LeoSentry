package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/policy/calendar"
)

var testVac = calendar.Vacations{
	Summer: calendar.Span{Start: "2026-07-01", End: "2026-08-31"},
	Winter: calendar.Span{Start: "2026-01-17", End: "2026-02-28"},
}

func TestNormalizeRejectsAmbiguity(t *testing.T) {
	cases := []struct {
		name string
		in   Save
		want string
	}{
		{"game above internet", Save{Enabled: true, DailyLimits: []DailyLimit{{Days: DaySelector{Kind: DayAll}, GameMinutes: 90, InternetMinutes: 60}}}, "游戏时长不能超过"},
		{"duplicate everyday", Save{Enabled: true, DailyLimits: []DailyLimit{
			{Days: DaySelector{Kind: DayAll}, GameMinutes: 30},
			{Days: DaySelector{Kind: DayAll}, GameMinutes: 60},
		}}, "都是每天"},
		{"weekday overlap", Save{Enabled: true, DailyLimits: []DailyLimit{
			{Days: DaySelector{Kind: DayWeekdays, Weekdays: []int{1, 2}}, GameMinutes: 30},
			{Days: DaySelector{Kind: DayWeekdays, Weekdays: []int{2, 3}}, GameMinutes: 60},
		}}, "会落在同一天"},
		{"summer missing", Save{Enabled: true, DailyLimits: []DailyLimit{{Days: DaySelector{Kind: DaySummer}, GameMinutes: 30}}}, "暑假"},
		{"zero length", Save{Enabled: true, Windows: []Window{{Days: DaySelector{Kind: DayAll}, Start: "10:00", End: "10:00", BlockGame: true}}}, "长度为 0"},
		{"schedule same rank overlap", Save{Enabled: true, Windows: []Window{
			{Days: DaySelector{Kind: DayWeekdays, Weekdays: []int{1}}, Start: "21:00", End: "22:00", BlockGame: true},
			{Days: DaySelector{Kind: DayWeekdays, Weekdays: []int{1, 2}}, Start: "22:00", End: "23:00", BlockVideo: true},
		}}, "会落在同一天"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(tc.in, calendar.Vacations{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	ok, err := Normalize(Save{
		Enabled: true,
		DailyLimits: []DailyLimit{
			{Days: DaySelector{Kind: DayAll}, InternetMinutes: 300},
			{Days: DaySelector{Kind: DayWorkday}, GameMinutes: 60, InternetMinutes: 120},
			{Days: DaySelector{Kind: DayRestday}, GameMinutes: 180},
			{Days: DaySelector{Kind: DayWeekdays, Weekdays: []int{1}}, GameMinutes: 30, InternetMinutes: 60},
		},
		Windows: []Window{
			{Days: DaySelector{Kind: DayWorkday}, Start: "21:30", End: "07:00", BlockInternet: true},
			{Days: DaySelector{Kind: DayRestday}, Start: "22:30", End: "8:00", BlockGame: true, BlockVideo: true},
		},
	}, testVac)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Windows[1].End != "08:00" || ok.DailyLimits[3].Days.Weekdays[0] != 1 {
		t.Fatalf("normalized = %+v", ok)
	}
}

func TestDecidePriority(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	work := DayInfo{Date: "2026-09-18", Weekday: 5, Workday: true}
	rest := DayInfo{Date: "2026-09-26", Weekday: 6, Workday: false, Name: "中秋节", Kind: "off"}
	makeup := DayInfo{Date: "2026-09-20", Weekday: 7, Workday: true, Name: "国庆节调休", Kind: "makeup"}
	summer := DayInfo{Date: "2026-07-15", Weekday: 3, Workday: true, Summer: true}
	spec := Spec{
		DailyLimits: []DailyLimit{
			{Days: DaySelector{Kind: DayWorkday}, GameMinutes: 60, InternetMinutes: 120},
			{Days: DaySelector{Kind: DayRestday}, GameMinutes: 30, InternetMinutes: 180},
			{Days: DaySelector{Kind: DaySummer}, GameMinutes: 90, InternetMinutes: 240},
			{Days: DaySelector{Kind: DayWeekdays, Weekdays: []int{1}}, GameMinutes: 20, InternetMinutes: 40},
		},
		Windows: []Window{
			{Days: DaySelector{Kind: DayWorkday}, Start: "21:30", End: "07:00", BlockGame: true, BlockVideo: true},
			{Days: DaySelector{Kind: DaySummer}, Start: "22:00", End: "08:00", BlockGame: true, BlockVideo: true},
		},
	}
	base := func(now time.Time, stat, today, yest DayInfo, usage Minutes) EvalInput {
		return EvalInput{
			Now: now, Enabled: true, Spec: spec, StatDay: stat, Today: today, Yesterday: yest, Usage: usage,
		}
	}

	fridayNight := time.Date(2026, 9, 18, 22, 0, 0, 0, loc)
	satEarly := time.Date(2026, 9, 19, 6, 30, 0, 0, loc)
	satLate := time.Date(2026, 9, 19, 9, 0, 0, 0, loc)
	midAutumn := time.Date(2026, 9, 26, 15, 0, 0, 0, loc)
	summerNight := time.Date(2026, 7, 15, 22, 30, 0, 0, loc)
	summerEarly := time.Date(2026, 7, 16, 6, 0, 0, 0, loc)
	monday := time.Date(2026, 9, 14, 12, 0, 0, 0, loc)
	mon := DayInfo{Date: "2026-09-14", Weekday: 1, Workday: true}

	cases := []struct {
		name           string
		in             EvalInput
		action, reason string
	}{
		{"under quota allows", base(time.Date(2026, 9, 18, 12, 0, 0, 0, loc), work, work, work, Minutes{20, 10, 0}), ActionAllow, ReasonNone},
		{"internet quota blocks all", base(time.Date(2026, 9, 18, 12, 0, 0, 0, loc), work, work, work, Minutes{120, 10, 0}), ActionBlockAll, ReasonInternet},
		{"game quota blocks game", base(time.Date(2026, 9, 18, 12, 0, 0, 0, loc), work, work, work, Minutes{80, 60, 0}), ActionBlockGame, ReasonGame},
		{"schedule before game", base(fridayNight, work, work, work, Minutes{80, 60, 0}), ActionBlockPart, ReasonSchedule},
		{"overnight uses previous workday", base(satEarly, work, rest, work, Minutes{}), ActionBlockPart, ReasonSchedule},
		{"morning after overnight is open", base(satLate, rest, rest, work, Minutes{10, 10, 0}), ActionAllow, ReasonNone},
		{"rest day quota", base(midAutumn, rest, rest, rest, Minutes{40, 30, 0}), ActionBlockGame, ReasonGame},
		{"makeup sunday is workday", base(time.Date(2026, 9, 20, 22, 0, 0, 0, loc), makeup, makeup, rest, Minutes{}), ActionBlockPart, ReasonSchedule},
		{"summer outranks workday at night", base(summerNight, summer, summer, summer, Minutes{}), ActionBlockPart, ReasonSchedule},
		{"summer overnight tail", base(summerEarly, summer, DayInfo{Date: "2026-07-16", Weekday: 4, Workday: true, Summer: true}, summer, Minutes{}), ActionBlockPart, ReasonSchedule},
		{"monday limit outranks workday", base(monday, mon, mon, mon, Minutes{30, 20, 0}), ActionBlockGame, ReasonGame},
		{"pause beats extend and quota", func() EvalInput {
			in := base(fridayNight, work, work, work, Minutes{200, 200, 0})
			in.Paused = true
			in.ExtendUntil = fridayNight.Add(time.Hour).Unix()
			return in
		}(), ActionBlockAll, ReasonPause},
		{"extend beats quota and schedule", func() EvalInput {
			in := base(fridayNight, work, work, work, Minutes{200, 200, 0})
			in.ExtendUntil = fridayNight.Add(30 * time.Minute).Unix()
			return in
		}(), ActionAllow, ReasonExtend},
		{"expired extend falls through", func() EvalInput {
			in := base(time.Date(2026, 9, 18, 12, 0, 0, 0, loc), work, work, work, Minutes{120, 10, 0})
			in.ExtendUntil = time.Date(2026, 9, 18, 11, 0, 0, 0, loc).Unix()
			return in
		}(), ActionBlockAll, ReasonInternet},
		{"disabled ignores quota", func() EvalInput {
			in := base(midAutumn, rest, rest, rest, Minutes{999, 999, 0})
			in.Enabled = false
			return in
		}(), ActionAllow, ReasonOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in)
			if got.Action != tc.action || got.Reason != tc.reason {
				t.Fatalf("action %s reason %s text %s", got.Action, got.Reason, got.Text)
			}
		})
	}

	mondayLimit := Decide(base(monday, mon, mon, mon, Minutes{}))
	if mondayLimit.GameLimit != 20 || mondayLimit.InternetLimit != 40 || mondayLimit.LimitLabel != "周一" {
		t.Fatalf("monday limit = %+v", mondayLimit)
	}
	summerLimit := Decide(base(summerNight, summer, summer, summer, Minutes{}))
	if summerLimit.GameLimit != 90 || summerLimit.LimitLabel != "暑假" {
		t.Fatalf("summer limit = %+v", summerLimit)
	}
	if !strings.Contains(Decide(base(summerNight, summer, summer, summer, Minutes{})).Text, "22:00–08:00") {
		t.Fatal("summer window should win over the workday window")
	}
}

func TestAllowModeOutsideWindow(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	work := DayInfo{Date: "2026-09-18", Weekday: 5, Workday: true}
	spec := Spec{Windows: []Window{{
		Days: DaySelector{Kind: DayWorkday}, Start: "18:00", End: "19:00", BlockInternet: true,
	}}}
	inside := Decide(EvalInput{Now: time.Date(2026, 9, 18, 18, 30, 0, 0, loc), Enabled: true, Spec: spec, StatDay: work, Today: work, Yesterday: work})
	if inside.Action != ActionBlockAll {
		t.Fatalf("inside = %s %s", inside.Action, inside.Text)
	}
	outside := Decide(EvalInput{Now: time.Date(2026, 9, 18, 20, 0, 0, 0, loc), Enabled: true, Spec: spec, StatDay: work, Today: work, Yesterday: work})
	if outside.Action != ActionAllow {
		t.Fatalf("outside = %s %s", outside.Action, outside.Text)
	}
	// 结束时刻不算在时段内。
	edge := Decide(EvalInput{Now: time.Date(2026, 9, 18, 19, 0, 0, 0, loc), Enabled: true, Spec: spec, StatDay: work, Today: work, Yesterday: work})
	if edge.Action != ActionAllow {
		t.Fatalf("edge = %s", edge.Action)
	}
	both := Decide(EvalInput{Now: time.Date(2026, 9, 18, 12, 0, 0, 0, loc), Enabled: true, Spec: Spec{Windows: []Window{{
		Days: DaySelector{Kind: DayAll}, Start: "00:00", End: "23:00", BlockGame: true, BlockVideo: true,
	}}}, StatDay: work, Today: work, Yesterday: work})
	if !both.BlockGame || !both.BlockVideo || both.Action != ActionBlockPart {
		t.Fatalf("both = %+v", both)
	}
}
