// Package calendar 解析中国大陆的年度日历。
// 法定节假日只存国务院办公厅公布的放假与调休，其余日期按星期推算；
// 暑假和寒假由用户指定，不来自国务院文件。
package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/leo/leosentry/assets"
)

const (
	version = 1
	region  = "CN"
)

// ErrNotFound 表示数据库里还没有该年的法定日历。
var ErrNotFound = errors.New("calendar not found")

// Error 是导入文件或寒暑假设置无法采用的原因，文案可直接展示。
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func fail(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// Span 是含首尾的日期区间，空串表示未设置。
type Span struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// Empty 报告区间是否未设置。
func (s Span) Empty() bool { return s.Start == "" && s.End == "" }

// Includes 报告 date（YYYY-MM-DD）是否落在区间内。
func (s Span) Includes(date string) bool {
	return !s.Empty() && date >= s.Start && date <= s.End
}

// Overlaps 报告两个区间是否有公共日期。
func (s Span) Overlaps(o Span) bool {
	return !s.Empty() && !o.Empty() && s.Start <= o.End && o.Start <= s.End
}

// Vacations 是自定义暑假和寒假。
type Vacations struct {
	Summer Span `json:"summer"`
	Winter Span `json:"winter"`
}

// Validate 检查寒暑假是否成对、真实且互不重叠。
func (v Vacations) Validate() error {
	if err := v.Summer.validate("暑假"); err != nil {
		return err
	}
	if err := v.Winter.validate("寒假"); err != nil {
		return err
	}
	if v.Summer.Overlaps(v.Winter) {
		return fail("暑假和寒假的日期重叠了")
	}
	return nil
}

func (s Span) validate(name string) error {
	if s.Empty() {
		return nil
	}
	if s.Start == "" || s.End == "" {
		return fail("请把%s的开始和结束日期都填上", name)
	}
	start, err := ParseDate(s.Start)
	if err != nil {
		return fail("%s的开始日期不正确", name)
	}
	end, err := ParseDate(s.End)
	if err != nil {
		return fail("%s的结束日期不正确", name)
	}
	if end.Before(start) {
		return fail("%s的结束日期不能早于开始日期", name)
	}
	if end.Sub(start) > 366*24*time.Hour {
		return fail("%s最长不能超过一年", name)
	}
	return nil
}

// Meta 是一份法定日历的来源说明。
type Meta struct {
	Year       int
	Region     string
	Source     string
	Document   string
	Published  string
	SourceURL  string
	ImportedAt int64
}

// Day 是公历中的一天。Kind 为 off（放假）或 makeup（调休上班），空串表示按星期推算。
type Day struct {
	Date    string
	Weekday int
	Workday bool
	Name    string
	Kind    string
}

// Year 是展开后的一年。Official 为 false 时表示尚未导入国务院数据，只按星期区分工作日。
type Year struct {
	Meta     Meta
	Days     []Day
	Official bool
	index    map[string]Day
}

// Get 按 YYYY-MM-DD 取一天。
func (y Year) Get(date string) (Day, bool) {
	if y.index != nil {
		d, ok := y.index[date]
		return d, ok
	}
	for _, d := range y.Days {
		if d.Date == date {
			return d, true
		}
	}
	return Day{}, false
}

// Reindex 在从数据库载入 Days 之后重建查找表。
func (y *Year) Reindex() { y.indexDays() }

func (y *Year) indexDays() {
	y.index = make(map[string]Day, len(y.Days))
	for _, d := range y.Days {
		y.index[d.Date] = d
	}
}

type file struct {
	Version   int    `json:"version"`
	Region    string `json:"region"`
	Year      int    `json:"year"`
	Source    string `json:"source"`
	Document  string `json:"document"`
	Published string `json:"published"`
	SourceURL string `json:"sourceURL"`
	Holidays  []struct {
		Name string   `json:"name"`
		Off  []string `json:"off"`
		Work []string `json:"work"`
	} `json:"holidays"`
	Vacations *Vacations `json:"vacations"`
}

// Parse 读取日历 JSON，展开为全年每一天。vac 非 nil 表示文件里写了寒暑假。
func Parse(data []byte) (Year, *Vacations, error) {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return Year{}, nil, fail("日历文件不是合法的 JSON")
	}
	if f.Version != version {
		return Year{}, nil, fail("不支持的日历版本")
	}
	if f.Region != region {
		return Year{}, nil, fail("只支持中国大陆（CN）日历")
	}
	if f.Year < 2000 || f.Year > 2100 {
		return Year{}, nil, fail("日历年份不正确")
	}
	if f.Source == "" || len([]rune(f.Source)) > 80 {
		return Year{}, nil, fail("请填写日历来源，且不超过 80 个字")
	}
	if len(f.Holidays) == 0 || len(f.Holidays) > 24 {
		return Year{}, nil, fail("节假日列表为空或过长")
	}
	if f.Vacations != nil {
		if err := f.Vacations.Validate(); err != nil {
			return Year{}, nil, err
		}
	}

	off := map[string]string{}
	work := map[string]string{}
	for _, h := range f.Holidays {
		if h.Name == "" || len([]rune(h.Name)) > 20 {
			return Year{}, nil, fail("节假日名称不正确")
		}
		if len(h.Off) == 0 {
			return Year{}, nil, fail("%s没有放假日期", h.Name)
		}
		if len(h.Off) > 20 || len(h.Work) > 8 {
			return Year{}, nil, fail("%s的日期太多", h.Name)
		}
		for _, d := range h.Off {
			if err := place(off, work, d, f.Year, h.Name); err != nil {
				return Year{}, nil, err
			}
		}
		label := h.Name
		if !strings.HasSuffix(label, "调休") {
			label += "调休"
		}
		for _, d := range h.Work {
			if err := place(work, off, d, f.Year, label); err != nil {
				return Year{}, nil, err
			}
		}
	}

	y := Year{
		Official: true,
		Meta: Meta{
			Year: f.Year, Region: f.Region, Source: f.Source, Document: f.Document,
			Published: f.Published, SourceURL: f.SourceURL,
		},
	}
	y.Days = expand(f.Year, func(date string, weekday int, baseWork bool) Day {
		if name, ok := off[date]; ok {
			return Day{Date: date, Weekday: weekday, Workday: false, Name: name, Kind: "off"}
		}
		if name, ok := work[date]; ok {
			return Day{Date: date, Weekday: weekday, Workday: true, Name: name, Kind: "makeup"}
		}
		return Day{Date: date, Weekday: weekday, Workday: baseWork}
	})
	y.indexDays()
	return y, f.Vacations, nil
}

func place(dst, other map[string]string, date string, year int, name string) error {
	t, err := ParseDate(date)
	if err != nil || t.Year() != year {
		return fail("%s的日期 %s 不属于 %d 年", name, date, year)
	}
	if _, ok := dst[date]; ok {
		return fail("%s 重复出现", date)
	}
	if _, ok := other[date]; ok {
		return fail("%s 不能同时放假和调休上班", date)
	}
	dst[date] = name
	return nil
}

func expand(year int, build func(date string, weekday int, workday bool) Day) []Day {
	start := time.Date(year, 1, 1, 12, 0, 0, 0, time.UTC)
	var days []Day
	for d := start; d.Year() == year; d = d.AddDate(0, 0, 1) {
		wd := isoWeekday(d)
		days = append(days, build(d.Format(time.DateOnly), wd, wd < 6))
	}
	return days
}

// WeekdayYear 生成没有法定调休的一年：周一至周五工作，周六日休息。
func WeekdayYear(year int) Year {
	y := Year{Meta: Meta{Year: year, Region: region}}
	y.Days = expand(year, func(date string, weekday int, workday bool) Day {
		return Day{Date: date, Weekday: weekday, Workday: workday}
	})
	y.indexDays()
	return y
}

// FallbackDay 按星期生成一天，用于还没有导入数据的日期。
func FallbackDay(date string) (Day, error) {
	t, err := ParseDate(date)
	if err != nil {
		return Day{}, err
	}
	wd := isoWeekday(t)
	return Day{Date: date, Weekday: wd, Workday: wd < 6}, nil
}

// ParseDate 解析 YYYY-MM-DD，拒绝 2 月 31 日这类被自动进位的日期。
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Format(time.DateOnly) != s {
		return time.Time{}, fail("日期 %s 不正确", s)
	}
	return t, nil
}

func isoWeekday(t time.Time) int {
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

// Template 返回内嵌日历原文。year 为 0 时返回年份最新的一份。
func Template(year int) ([]byte, string, error) {
	entries, err := fs.ReadDir(assets.Calendar, "calendar")
	if err != nil {
		return nil, "", err
	}
	var best []byte
	bestYear := -1
	bestName := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(assets.Calendar, "calendar/"+e.Name())
		if err != nil {
			return nil, "", err
		}
		y, _, err := Parse(b)
		if err != nil {
			return nil, "", err
		}
		if year != 0 && y.Meta.Year == year {
			return b, e.Name(), nil
		}
		if y.Meta.Year > bestYear {
			best, bestYear, bestName = b, y.Meta.Year, e.Name()
		}
	}
	if year != 0 || best == nil {
		return nil, "", ErrNotFound
	}
	return best, bestName, nil
}
