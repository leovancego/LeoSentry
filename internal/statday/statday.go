// Package statday 定义"统计日"：以每日切换时刻（默认 03:00）为界的一天。
// today.db 的切换、内存当日计数器的清零、归档文件的命名都以它为准，
// 这样深夜 0~3 点的使用会计入前一天，与家长对"今天"的直觉一致。
package statday

import "time"

// Calendar 根据切换时刻与时区计算统计日边界。
type Calendar struct {
	hour, minute int
	loc          *time.Location
}

// New 创建统计日日历。loc 为 nil 时使用 time.Local。
func New(hour, minute int, loc *time.Location) Calendar {
	if loc == nil {
		loc = time.Local
	}
	return Calendar{hour: hour, minute: minute, loc: loc}
}

// DayStart 返回 t 所属统计日的起始时刻。
func (c Calendar) DayStart(t time.Time) time.Time {
	t = t.In(c.loc)
	start := time.Date(t.Year(), t.Month(), t.Day(), c.hour, c.minute, 0, 0, c.loc)
	if t.Before(start) {
		start = start.AddDate(0, 0, -1)
	}
	return start
}

// NextStart 返回 t 之后的下一个统计日起始时刻。
func (c Calendar) NextStart(t time.Time) time.Time {
	return c.DayStart(t).AddDate(0, 0, 1)
}

// Label 返回统计日的日期标签（YYYY-MM-DD），用于归档文件命名。
func (c Calendar) Label(dayStart time.Time) string {
	return dayStart.In(c.loc).Format(time.DateOnly)
}

// Location 返回日历使用的时区。
func (c Calendar) Location() *time.Location {
	return c.loc
}
