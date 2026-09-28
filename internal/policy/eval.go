package policy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/policy/calendar"
)

const (
	maxDailyLimits   = 8
	maxWindows       = 12
	maxExtendMinutes = 180

	DayAll      = "all"
	DayWorkday  = "workday"
	DayRestday  = "restday"
	DaySummer   = "summer"
	DayWinter   = "winter"
	DayWeekdays = "weekdays"

	ActionAllow      = "allow"
	ActionBlockAll   = "block_all"
	ActionBlockGame  = "block_game"
	ActionBlockVideo = "block_video"
	ActionBlockPart  = "block_part"

	ReasonOff      = "off"
	ReasonNone     = "none"
	ReasonPause    = "pause"
	ReasonExtend   = "extend"
	ReasonInternet = "internet"
	ReasonSchedule = "schedule"
	ReasonGame     = "game"
	ReasonVideo    = "video"
)

// InputError 是用户可以改正的策略或日历错误。
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func inputf(format string, args ...any) error {
	return &InputError{Message: fmt.Sprintf(format, args...)}
}

// DaySelector 选择策略适用的日期。星期为 1（周一）到 7（周日）。
type DaySelector struct {
	Kind     string `json:"kind"`
	Weekdays []int  `json:"weekdays,omitempty"`
}

// DailyLimit 是某一类日期上的时长上限，0 表示这一项不限制。
type DailyLimit struct {
	Days            DaySelector `json:"days"`
	GameMinutes     int         `json:"gameMinutes,omitempty"`
	VideoMinutes    int         `json:"videoMinutes,omitempty"`
	InternetMinutes int         `json:"internetMinutes,omitempty"`
}

// Window 是一段独立的时段控制。结束早于开始表示持续到次日，例如 21:30–07:00。
// 禁止上网、禁止游戏、禁止视频都写在这一段上；游戏和视频可以同时选。
// 三项都空表示这一段不额外限制。
type Window struct {
	Days          DaySelector `json:"days"`
	Start         string      `json:"start"`
	End           string      `json:"end"`
	BlockInternet bool        `json:"blockInternet,omitempty"`
	BlockGame     bool        `json:"blockGame,omitempty"`
	BlockVideo    bool        `json:"blockVideo,omitempty"`
}

// Spec 是一台设备上可保存的时长和时段规则，不含暂停和临时延长。
type Spec struct {
	DailyLimits []DailyLimit `json:"dailyLimits,omitempty"`
	Windows     []Window     `json:"windows,omitempty"`
}

// Save 是策略编辑器提交的内容。
type Save struct {
	Enabled     bool         `json:"enabled"`
	DailyLimits []DailyLimit `json:"dailyLimits"`
	Windows     []Window     `json:"windows"`
}

// SpecOf 返回保存请求里的规则部分。
func (s Save) SpecOf() Spec {
	return Spec{DailyLimits: s.DailyLimits, Windows: s.Windows}
}

// Minutes 是统计日已经累计的活跃分钟。
type Minutes struct {
	Internet int `json:"internet"`
	Game     int `json:"game"`
	Video    int `json:"video"`
}

// DayInfo 是评估时用到的一天。
type DayInfo struct {
	Date    string
	Weekday int
	Workday bool
	Name    string
	Kind    string
	Summer  bool
	Winter  bool
}

// Decision 是一台设备此刻的判定。
type Decision struct {
	Controlled    bool
	Enabled       bool
	Paused        bool
	ExtendUntil   int64
	Spec          Spec
	Usage         Minutes
	GameLimit     int
	VideoLimit    int
	InternetLimit int
	LimitLabel    string
	Action        string
	BlockGame     bool
	BlockVideo    bool
	Reason        string
	Text          string
}

// EvalInput 是一次判定的全部输入。暂停和未过期的临时延长优先于其他规则。
type EvalInput struct {
	Now         time.Time
	Enabled     bool
	Paused      bool
	ExtendUntil int64
	Spec        Spec
	StatDay     DayInfo
	Today       DayInfo
	Yesterday   DayInfo
	Usage       Minutes
}

// Normalize 整理并拒绝有歧义的策略。同一天的时长和时段都只按一套规则理解：
// 暑假/寒假优先于指定星期，再优先于法定工作日/休息日，最后是每天。
func Normalize(in Save, vac calendar.Vacations) (Save, error) {
	if len(in.DailyLimits) > maxDailyLimits {
		return Save{}, inputf("每日上限最多 %d 条", maxDailyLimits)
	}
	if len(in.Windows) > maxWindows {
		return Save{}, inputf("时段最多 %d 段", maxWindows)
	}
	for i := range in.DailyLimits {
		sel, err := normalizeSelector(in.DailyLimits[i].Days, vac)
		if err != nil {
			return Save{}, err
		}
		in.DailyLimits[i].Days = sel
		g, v, n := in.DailyLimits[i].GameMinutes, in.DailyLimits[i].VideoMinutes, in.DailyLimits[i].InternetMinutes
		if g < 0 || v < 0 || n < 0 || g > 24*60 || v > 24*60 || n > 24*60 {
			return Save{}, inputf("时长不能超过 24 小时（1440 分钟），留空表示不限制")
		}
		if g == 0 && v == 0 && n == 0 {
			return Save{}, inputf("每条每日上限至少填写游戏、视频或上网其中一项")
		}
		if n > 0 && g > n {
			return Save{}, inputf("游戏时长不能超过上网时长，否则上网先到上限")
		}
		if n > 0 && v > n {
			return Save{}, inputf("视频时长不能超过上网时长，否则上网先到上限")
		}
	}
	if err := rejectLimitClash(in.DailyLimits, vac); err != nil {
		return Save{}, err
	}
	seen := map[string]struct{}{}
	for i := range in.Windows {
		sel, err := normalizeSelector(in.Windows[i].Days, vac)
		if err != nil {
			return Save{}, err
		}
		start, end, err := normalizeClock(in.Windows[i].Start, in.Windows[i].End)
		if err != nil {
			return Save{}, err
		}
		in.Windows[i].Days = sel
		in.Windows[i].Start = start
		in.Windows[i].End = end
		if in.Windows[i].BlockInternet {
			in.Windows[i].BlockGame = false
			in.Windows[i].BlockVideo = false
		}
		key := selectorKey(sel) + "|" + start + "|" + end
		if _, ok := seen[key]; ok {
			return Save{}, inputf("时段 %s–%s 重复了", start, end)
		}
		seen[key] = struct{}{}
	}
	if err := rejectScheduleClash(in.Windows, vac); err != nil {
		return Save{}, err
	}
	return in, nil
}

func normalizeSelector(s DaySelector, vac calendar.Vacations) (DaySelector, error) {
	switch s.Kind {
	case DayAll, DayWorkday, DayRestday:
		if len(s.Weekdays) > 0 {
			return DaySelector{}, inputf("%s不用再选星期", s.label())
		}
		s.Weekdays = nil
	case DayWeekdays:
		if len(s.Weekdays) == 0 {
			return DaySelector{}, inputf("请选择星期")
		}
		var days []int
		seen := map[int]struct{}{}
		for _, d := range s.Weekdays {
			if d < 1 || d > 7 {
				return DaySelector{}, inputf("星期要选周一到周日")
			}
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			days = append(days, d)
		}
		slices.Sort(days)
		s.Weekdays = days
	case DaySummer:
		if vac.Summer.Empty() {
			return DaySelector{}, inputf("请先在日历里填写暑假的开始和结束日期")
		}
		s.Weekdays = nil
	case DayWinter:
		if vac.Winter.Empty() {
			return DaySelector{}, inputf("请先在日历里填写寒假的开始和结束日期")
		}
		s.Weekdays = nil
	default:
		return DaySelector{}, inputf("不认识的日期类型")
	}
	return s, nil
}

func normalizeClock(start, end string) (string, string, error) {
	sm, err := parseClock(start, false)
	if err != nil {
		return "", "", inputf("开始时间 %s 不正确，请用 21:30 这种格式", start)
	}
	em, err := parseClock(end, true)
	if err != nil {
		return "", "", inputf("结束时间 %s 不正确，请用 07:00 或 24:00", end)
	}
	if sm == em || (sm == 0 && em == 0) {
		return "", "", inputf("时段 %s–%s 的长度为 0", formatClock(sm), formatClock(em))
	}
	return formatClock(sm), formatClock(em), nil
}

func parseClock(s string, allowMidnight bool) (int, error) {
	s = strings.TrimSpace(s)
	if allowMidnight && (s == "24:00" || s == "24:0") {
		return 24 * 60, nil
	}
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) == 0 || len(h) > 2 || len(m) == 0 || len(m) > 2 {
		return 0, fmt.Errorf("clock")
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("clock")
	}
	return hh*60 + mm, nil
}

func formatClock(mins int) string {
	if mins == 24*60 {
		return "24:00"
	}
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

func rejectLimitClash(limits []DailyLimit, vac calendar.Vacations) error {
	seen := map[string]int{}
	for i, l := range limits {
		key := selectorKey(l.Days)
		if j, ok := seen[key]; ok {
			return inputf("第 %d 条和第 %d 条每日上限都是%s，请合并成一条", j+1, i+1, l.Days.label())
		}
		seen[key] = i
	}
	for i := range limits {
		for j := i + 1; j < len(limits); j++ {
			if rank(limits[i].Days.Kind) != rank(limits[j].Days.Kind) {
				continue
			}
			if overlaps(limits[i].Days, limits[j].Days, vac) {
				return inputf("%s和%s会落在同一天，无法确定用哪一条时长上限", limits[i].Days.label(), limits[j].Days.label())
			}
		}
	}
	return nil
}

func rejectScheduleClash(windows []Window, vac calendar.Vacations) error {
	for i := range windows {
		for j := i + 1; j < len(windows); j++ {
			if selectorKey(windows[i].Days) == selectorKey(windows[j].Days) {
				continue
			}
			if rank(windows[i].Days.Kind) != rank(windows[j].Days.Kind) {
				continue
			}
			if overlaps(windows[i].Days, windows[j].Days, vac) {
				return inputf("时段里的%s和%s会落在同一天。同一级日期请写成互不重叠的几段，更具体的日期会自动盖过更宽的日期", windows[i].Days.label(), windows[j].Days.label())
			}
		}
	}
	return nil
}

// 数字越大越具体。同一天只采用最具体的一组。
func rank(kind string) int {
	switch kind {
	case DaySummer, DayWinter:
		return 3
	case DayWeekdays:
		return 2
	case DayWorkday, DayRestday:
		return 1
	case DayAll:
		return 0
	default:
		return -1
	}
}

func selectorKey(s DaySelector) string {
	if s.Kind == DayWeekdays {
		return fmt.Sprintf("weekdays:%d", weekdayBits(s.Weekdays))
	}
	return s.Kind
}

func weekdayBits(days []int) uint8 {
	var b uint8
	for _, d := range days {
		if d >= 1 && d <= 7 {
			b |= 1 << d
		}
	}
	return b
}

// overlaps 只在两种日期类型确实可能是同一天时返回 true。
// 调休会使“周日”同时是工作日，法定假日会使“周一”同时是休息日，所以指定星期和工作日/休息日算重叠。
func overlaps(a, b DaySelector, vac calendar.Vacations) bool {
	if a.Kind == DayAll || b.Kind == DayAll {
		return true
	}
	if a.Kind == DayWorkday && b.Kind == DayRestday || a.Kind == DayRestday && b.Kind == DayWorkday {
		return false
	}
	if a.Kind == DayWeekdays && b.Kind == DayWeekdays {
		return weekdayBits(a.Weekdays)&weekdayBits(b.Weekdays) != 0
	}
	if a.Kind == DaySummer && b.Kind == DayWinter || a.Kind == DayWinter && b.Kind == DaySummer {
		return vac.Summer.Overlaps(vac.Winter)
	}
	return true
}

func (s DaySelector) label() string {
	switch s.Kind {
	case DayAll:
		return "每天"
	case DayWorkday:
		return "法定工作日"
	case DayRestday:
		return "法定休息日"
	case DaySummer:
		return "暑假"
	case DayWinter:
		return "寒假"
	case DayWeekdays:
		names := make([]string, 0, len(s.Weekdays))
		for _, d := range s.Weekdays {
			names = append(names, weekdayName(d))
		}
		if len(names) == 0 {
			return "指定星期"
		}
		return strings.Join(names, "、")
	default:
		return "日期"
	}
}

func weekdayName(d int) string {
	names := []string{"", "周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	if d < 1 || d > 7 {
		return ""
	}
	return names[d]
}

func matches(s DaySelector, day DayInfo) bool {
	switch s.Kind {
	case DayAll:
		return true
	case DayWorkday:
		return day.Workday
	case DayRestday:
		return !day.Workday
	case DaySummer:
		return day.Summer
	case DayWinter:
		return day.Winter
	case DayWeekdays:
		return slices.Contains(s.Weekdays, day.Weekday)
	default:
		return false
	}
}

// Decide 按固定顺序判定：暂停、临时延长、上网时长、娱乐时段、游戏时长。
// 恢复暂停或延长结束后，调用方传入的用量原样参与后面的判断。
func Decide(in EvalInput) Decision {
	d := Decision{
		Enabled: in.Enabled, Paused: in.Paused, ExtendUntil: in.ExtendUntil,
		Spec: in.Spec, Usage: in.Usage, Action: ActionAllow, Reason: ReasonOff,
		Text: "未启用管控",
	}
	if lim, ok := pickLimit(in.Spec.DailyLimits, in.StatDay); ok {
		d.GameLimit = lim.GameMinutes
		d.VideoLimit = lim.VideoMinutes
		d.InternetLimit = lim.InternetMinutes
		d.LimitLabel = lim.Days.label()
	}
	extendActive := in.ExtendUntil > in.Now.Unix()
	d.Controlled = in.Enabled || in.Paused || extendActive
	if !d.Controlled {
		return d
	}
	if in.Paused {
		d.Action = ActionBlockAll
		d.Reason = ReasonPause
		d.Text = "已暂停上网"
		return d
	}
	if extendActive {
		d.Action = ActionAllow
		d.Reason = ReasonExtend
		until := time.Unix(in.ExtendUntil, 0).In(in.Now.Location())
		if until.Format(time.DateOnly) == in.Now.Format(time.DateOnly) {
			d.Text = "临时延长至 " + until.Format("15:04") + "，这期间不限制时长和时段"
		} else {
			d.Text = "临时延长至 " + until.Format("1月2日 15:04") + "，这期间不限制时长和时段"
		}
		return d
	}
	if d.InternetLimit > 0 && in.Usage.Internet >= d.InternetLimit {
		d.Action = ActionBlockAll
		d.Reason = ReasonInternet
		d.Text = fmt.Sprintf("今日上网已满 %d 分钟，已禁止上网", d.InternetLimit)
		return d
	}
	hit := activeRestrict(in.Spec.Windows, in.Now, in.Today, in.Yesterday)
	gameQuota := d.GameLimit > 0 && in.Usage.Game >= d.GameLimit
	videoQuota := d.VideoLimit > 0 && in.Usage.Video >= d.VideoLimit
	if hit.Internet {
		d.Action = ActionBlockAll
		d.Reason = ReasonSchedule
		d.Text = "当前时段禁止上网"
		if hit.Label != "" {
			d.Text += "（" + hit.Label + "）"
		}
		return d
	}
	blockGame := hit.Game || gameQuota
	blockVideo := hit.Video || videoQuota
	d.BlockGame = blockGame
	d.BlockVideo = blockVideo
	if !blockGame && !blockVideo {
		d.Action = ActionAllow
		d.Reason = ReasonNone
		d.Text = "当前允许上网"
		return d
	}
	d.Action = actionOf(blockGame, blockVideo)
	d.Reason = reasonOf(hit, gameQuota, videoQuota)
	d.Text = restrictText(hit, gameQuota, videoQuota, d.GameLimit, d.VideoLimit)
	return d
}

func actionOf(game, video bool) string {
	switch {
	case game && video:
		return ActionBlockPart
	case game:
		return ActionBlockGame
	default:
		return ActionBlockVideo
	}
}

func reasonOf(hit restrictHit, gameQuota, videoQuota bool) string {
	if hit.Game || hit.Video {
		return ReasonSchedule
	}
	if gameQuota {
		return ReasonGame
	}
	return ReasonVideo
}

func restrictText(hit restrictHit, gameQuota, videoQuota bool, gameLimit, videoLimit int) string {
	var parts []string
	if hit.Game || hit.Video {
		var names []string
		if hit.Game {
			names = append(names, "游戏")
		}
		if hit.Video {
			names = append(names, "视频")
		}
		text := "当前时段禁止" + strings.Join(names, "、")
		if hit.Label != "" {
			text += "（" + hit.Label + "）"
		}
		parts = append(parts, text)
	}
	if gameQuota && !hit.Game {
		parts = append(parts, fmt.Sprintf("今日游戏已满 %d 分钟，已禁止游戏", gameLimit))
	}
	if videoQuota && !hit.Video {
		parts = append(parts, fmt.Sprintf("今日视频已满 %d 分钟，已禁止视频", videoLimit))
	}
	if len(parts) == 0 {
		if gameQuota && videoQuota {
			return fmt.Sprintf("今日游戏已满 %d 分钟、视频已满 %d 分钟", gameLimit, videoLimit)
		}
		if gameQuota {
			return fmt.Sprintf("今日游戏已满 %d 分钟，已禁止游戏", gameLimit)
		}
		return fmt.Sprintf("今日视频已满 %d 分钟，已禁止视频", videoLimit)
	}
	return strings.Join(parts, "；")
}

func pickLimit(limits []DailyLimit, day DayInfo) (DailyLimit, bool) {
	bestRank := -1
	var best DailyLimit
	found := false
	for _, l := range limits {
		if !matches(l.Days, day) {
			continue
		}
		r := rank(l.Days.Kind)
		if !found || r > bestRank || (r == bestRank && stricter(l, best)) {
			best, bestRank, found = l, r, true
		}
	}
	return best, found
}

func stricter(a, b DailyLimit) bool {
	as, bs := score(a), score(b)
	return as < bs
}

func score(l DailyLimit) int {
	g, v, n := l.GameMinutes, l.VideoMinutes, l.InternetMinutes
	if g == 0 {
		g = 100000
	}
	if v == 0 {
		v = 100000
	}
	if n == 0 {
		n = 100000
	}
	return g + v + n
}

type restrictHit struct {
	Internet bool
	Game     bool
	Video    bool
	Label    string
}

func (r restrictHit) merge(o restrictHit) restrictHit {
	out := restrictHit{
		Internet: r.Internet || o.Internet,
		Game:     r.Game || o.Game,
		Video:    r.Video || o.Video,
		Label:    r.Label,
	}
	if out.Label == "" {
		out.Label = o.Label
	}
	if out.Internet {
		out.Game, out.Video = false, false
	}
	return out
}

func activeRestrict(windows []Window, now time.Time, today, yesterday DayInfo) restrictHit {
	nowM := now.Hour()*60 + now.Minute()
	return coversDay(windows, nowM, today, false).merge(coversDay(windows, nowM, yesterday, true))
}

// tail 为 true 时只看跨到次日的那一段，日期类型按开始那天计算。
func coversDay(windows []Window, nowM int, day DayInfo, tail bool) restrictHit {
	best := -1
	for _, w := range windows {
		if matches(w.Days, day) && rank(w.Days.Kind) > best {
			best = rank(w.Days.Kind)
		}
	}
	var hit restrictHit
	if best < 0 {
		return hit
	}
	for _, w := range windows {
		if rank(w.Days.Kind) != best || !matches(w.Days, day) || !windowCovers(w, nowM, tail) {
			continue
		}
		if !w.BlockInternet && !w.BlockGame && !w.BlockVideo {
			continue
		}
		label := w.Start + "–" + w.End
		if _, _, overnight := bounds(w); overnight {
			label += "（至次日）"
		}
		hit = hit.merge(restrictHit{
			Internet: w.BlockInternet, Game: w.BlockGame, Video: w.BlockVideo, Label: label,
		})
	}
	return hit
}

func windowCovers(w Window, nowM int, tail bool) bool {
	start, end, overnight := bounds(w)
	if tail {
		return overnight && nowM < end
	}
	if overnight {
		return nowM >= start
	}
	return nowM >= start && nowM < end
}

func bounds(w Window) (start, end int, overnight bool) {
	start, _ = parseClock(w.Start, false)
	end, _ = parseClock(w.End, true)
	if end == 0 && start > 0 {
		return start, 24 * 60, false
	}
	if end < start {
		return start, end, true
	}
	return start, end, false
}

// UsesKind 报告规则是否引用了暑假或寒假。
func UsesKind(spec Spec, kind string) bool {
	for _, l := range spec.DailyLimits {
		if l.Days.Kind == kind {
			return true
		}
	}
	for _, w := range spec.Windows {
		if w.Days.Kind == kind {
			return true
		}
	}
	return false
}

// HasRestriction 报告除暂停以外是否还有会拦住设备的规则。
func HasRestriction(spec Spec) bool {
	if len(spec.DailyLimits) > 0 {
		return true
	}
	for _, w := range spec.Windows {
		if w.BlockInternet || w.BlockGame || w.BlockVideo {
			return true
		}
	}
	return false
}
