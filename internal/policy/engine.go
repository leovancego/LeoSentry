package policy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/device"
	"github.com/leo/leosentry/internal/model"
	"github.com/leo/leosentry/internal/nftctl"
	"github.com/leo/leosentry/internal/policy/calendar"
	"github.com/leo/leosentry/internal/statday"
	"github.com/leo/leosentry/internal/store"
)

// CheckInterval 是自动检查用量并下发防火墙的间隔。
const CheckInterval = 5 * time.Minute

// ErrNotFound 表示要操作的策略不存在。
var ErrNotFound = errors.New("policy not found")

// Firewall 把期望地址下发到 nftables。没有变化时返回的 changed 为 false。
type Firewall interface {
	Apply(nftctl.Targets) (changed bool, err error)
}

// Addresses 提供设备当前的 IPv4 地址。
type Addresses interface {
	Devices() []model.Device
}

// Domains 提供 DNS 观察到的 IPv4 → 域名。
type Domains interface {
	Snapshot(time.Time) map[netip.Addr]string
}

// UsageSource 统计受管控设备今天的用量。
type UsageSource interface {
	Minutes(ctx context.Context, macs []string) (map[string]Minutes, error)
}

// Options 配置策略引擎。
type Options struct {
	DB        *store.PolicyStore
	Usage     UsageSource
	Addresses Addresses
	Domains   Domains
	Rules     *category.Matcher
	Stat      statday.Calendar
	Firewall  Firewall
	Logger    *slog.Logger
	Now       func() time.Time
	Interval  time.Duration
	// Live 可热更新规则。为空时使用 Rules。
	Live *category.Live
}

// Engine 每 5 分钟评估一次受管控设备，只在期望结果变化时更新 nft 集合。
type Engine struct {
	db       *store.PolicyStore
	usage    UsageSource
	addrs    Addresses
	domains  Domains
	rules    *category.Live
	stat     statday.Calendar
	fw       Firewall
	log      *slog.Logger
	now      func() time.Time
	interval time.Duration
	poke     chan struct{}

	mu   sync.Mutex
	snap Snapshot
}

// New 创建策略引擎。
func New(opts Options) *Engine {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Interval <= 0 {
		opts.Interval = CheckInterval
	}
	live := opts.Live
	if live == nil {
		live = category.NewLive(opts.Rules)
	}
	return &Engine{
		db: opts.DB, usage: opts.Usage, addrs: opts.Addresses, domains: opts.Domains,
		rules: live, stat: opts.Stat, fw: opts.Firewall, log: opts.Logger,
		now: opts.Now, interval: opts.Interval, poke: make(chan struct{}, 1),
	}
}

// SetCheckInterval 修改自动检查间隔，下一次等待按新间隔计算。
func (e *Engine) SetCheckInterval(d time.Duration) {
	if d < time.Second || d > time.Hour {
		return
	}
	e.mu.Lock()
	e.interval = d
	e.mu.Unlock()
	select {
	case e.poke <- struct{}{}:
	default:
	}
}

// CheckInterval 返回当前自动检查间隔。
func (e *Engine) CheckInterval() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.interval
}

// Run 直到 ctx 取消。启动前应先调用一次 Check，这里按间隔重复检查。
func (e *Engine) Run(ctx context.Context) {
	timer := time.NewTimer(e.CheckInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.poke:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(e.CheckInterval())
		case <-timer.C:
			if _, err := e.Check(ctx); err != nil && e.log != nil {
				e.log.Error("policy check failed", "err", err)
			}
			timer.Reset(e.CheckInterval())
		}
	}
}

// Current 返回最近一次检查。还没有检查过时先做一次。
func (e *Engine) Current(ctx context.Context) (Snapshot, error) {
	e.mu.Lock()
	ready := !e.snap.CheckedAt.IsZero()
	snap := e.snap
	e.mu.Unlock()
	if ready {
		return snap, nil
	}
	return e.Check(ctx)
}

// Check 重新计算全部受管控设备，并在结果变化时更新防火墙。
func (e *Engine) Check(ctx context.Context) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkLocked(ctx)
}

func (e *Engine) checkLocked(ctx context.Context) (Snapshot, error) {
	now := e.now().In(e.stat.Location())
	civil := now.Format(time.DateOnly)
	yesterday := now.AddDate(0, 0, -1).Format(time.DateOnly)
	statLabel := e.stat.Label(e.stat.DayStart(now))
	vac, err := e.db.Vacations(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	years := map[int]calendar.Year{}
	for _, date := range []string{civil, yesterday, statLabel} {
		y, err := calendar.ParseDate(date)
		if err != nil {
			return Snapshot{}, err
		}
		if _, ok := years[y.Year()]; ok {
			continue
		}
		loaded, err := e.year(ctx, y.Year())
		if err != nil {
			return Snapshot{}, err
		}
		years[y.Year()] = loaded
	}
	todayInfo := describe(years[mustYear(civil)], vac, civil)
	yestInfo := describe(years[mustYear(yesterday)], vac, yesterday)
	statInfo := describe(years[mustYear(statLabel)], vac, statLabel)

	if err := e.db.ClearExpiredExtends(ctx, now.Unix()); err != nil && e.log != nil {
		e.log.Warn("clear expired extends", "err", err)
	}
	rows, err := e.db.Policies(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	var macs []string
	for _, row := range rows {
		if row.Enabled {
			macs = append(macs, row.MAC)
		}
	}
	usage := map[string]Minutes{}
	if e.usage != nil && len(macs) > 0 {
		usage, err = e.usage.Minutes(ctx, macs)
		if err != nil {
			if e.log != nil {
				e.log.Error("policy usage", "err", err)
			}
			if e.snap.CheckedAt.IsZero() {
				return Snapshot{}, err
			}
			e.snap.EnforceError = "没能读到今天的用量，网络控制仍保持上次的结果"
			return e.snap, nil
		}
	}
	if usage == nil {
		usage = map[string]Minutes{}
	}

	by := make(map[string]Decision, len(rows))
	for _, row := range rows {
		spec, decErr := decodeSpec(row.Spec)
		if decErr != nil {
			by[row.MAC] = Decision{
				Controlled: true, Enabled: row.Enabled, Paused: row.Paused,
				Action: ActionBlockAll, Reason: "invalid", Text: "策略数据损坏，已先断网",
			}
			continue
		}
		by[row.MAC] = Decide(EvalInput{
			Now: now, Enabled: row.Enabled, Paused: row.Paused, ExtendUntil: row.ExtendUntil,
			Spec: spec, StatDay: statInfo, Today: todayInfo, Yesterday: yestInfo, Usage: usage[row.MAC],
		})
	}

	ips := map[string][]netip.Addr{}
	if e.addrs != nil {
		for _, d := range e.addrs.Devices() {
			if d.MAC == "" || !d.IP.IsValid() || !d.IP.Is4() {
				continue
			}
			ips[d.MAC] = append(ips[d.MAC], d.IP)
		}
	}
	var targets nftctl.Targets
	needGame, needVideo := false, false
	for mac, dec := range by {
		if dec.Action == ActionAllow {
			continue
		}
		if dec.Action == ActionBlockAll && mac != "" {
			targets.BlockAllMAC = append(targets.BlockAllMAC, mac)
		}
		if len(ips[mac]) == 0 {
			if dec.Action != ActionBlockAll {
				dec.Text += "。设备当前没有 IP，得到地址后的下一次检查会生效"
				by[mac] = dec
			}
			continue
		}
		if dec.Action == ActionBlockAll {
			targets.BlockAll = append(targets.BlockAll, ips[mac]...)
			continue
		}
		if dec.BlockGame {
			targets.BlockGame = append(targets.BlockGame, ips[mac]...)
			needGame = true
		}
		if dec.BlockVideo {
			targets.BlockVideo = append(targets.BlockVideo, ips[mac]...)
			needVideo = true
		}
	}
	if (needGame || needVideo) && e.rules != nil && e.domains != nil {
		if rules := e.rules.Get(); rules != nil {
			game, video := classify(rules, e.domains.Snapshot(now))
			if needGame {
				targets.GameDest = game
			}
			if needVideo {
				targets.VideoDest = video
			}
		}
	}

	_, off := now.Zone()
	snap := Snapshot{
		CheckedAt: now, TZOffset: off, StatDay: statLabel, CivilToday: civil,
		Firewall: e.fw != nil, ByMAC: by,
		Calendar: calendarView(years[mustYear(civil)], vac, todayInfo, statInfo),
	}
	if e.fw != nil {
		changed, err := e.fw.Apply(targets)
		if err != nil {
			snap.EnforceError = "防火墙更新失败，仍保持上次的限制，将自动重试"
			if e.log != nil {
				e.log.Error("policy enforce failed", "err", err)
			}
		} else if changed && e.log != nil {
			e.log.Info("policy applied",
				"block_all", len(targets.BlockAll),
				"block_all_mac", len(targets.BlockAllMAC),
				"block_video", len(targets.BlockVideo),
				"block_game", len(targets.BlockGame),
				"game_dest", len(targets.GameDest),
				"video_dest", len(targets.VideoDest))
		}
	}
	e.snap = snap
	return snap, nil
}

func (e *Engine) year(ctx context.Context, year int) (calendar.Year, error) {
	y, err := e.db.Year(ctx, year)
	if errors.Is(err, calendar.ErrNotFound) {
		return calendar.WeekdayYear(year), nil
	}
	return y, err
}

// ImportCalendar 导入一年的法定日历。文件里如果带了寒暑假，会一并覆盖。
func (e *Engine) ImportCalendar(ctx context.Context, data []byte) (Snapshot, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	y, vac, err := calendar.Parse(data)
	if err != nil {
		return Snapshot{}, asInput(err)
	}
	if vac != nil {
		if err := e.vacationStillUsable(ctx, *vac); err != nil {
			return Snapshot{}, err
		}
	}
	if err := e.db.SaveCalendar(ctx, y, vac, e.now()); err != nil {
		return Snapshot{}, asInput(err)
	}
	return e.Check(ctx)
}

// SaveVacations 保存自定义暑假和寒假。
func (e *Engine) SaveVacations(ctx context.Context, v calendar.Vacations) (Snapshot, error) {
	if err := asInput(v.Validate()); err != nil {
		return Snapshot{}, err
	}
	if err := e.vacationStillUsable(ctx, v); err != nil {
		return Snapshot{}, err
	}
	if err := e.db.SaveVacations(ctx, v); err != nil {
		return Snapshot{}, asInput(err)
	}
	return e.Check(ctx)
}

// Save 保存一台设备的时长和时段。关闭管控时会取消暂停和临时延长。
func (e *Engine) Save(ctx context.Context, mac string, in Save) (Snapshot, error) {
	mac, err := normMAC(mac)
	if err != nil {
		return Snapshot{}, err
	}
	vac, err := e.db.Vacations(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	in, err = Normalize(in, vac)
	if err != nil {
		return Snapshot{}, err
	}
	raw, err := json.Marshal(in.SpecOf())
	if err != nil {
		return Snapshot{}, err
	}
	if err := e.db.SaveSpec(ctx, mac, in.Enabled, string(raw), e.now().Unix()); err != nil {
		return Snapshot{}, err
	}
	return e.Check(ctx)
}

// Pause 暂停或恢复一台设备的外网。恢复后不改用量，下一次判定按原策略执行。
func (e *Engine) Pause(ctx context.Context, mac string, paused bool) (Snapshot, error) {
	mac, err := normMAC(mac)
	if err != nil {
		return Snapshot{}, err
	}
	if err := e.db.SetPause(ctx, mac, paused, e.now().Unix()); err != nil {
		return Snapshot{}, err
	}
	return e.Check(ctx)
}

// Extend 从现在起临时延长游玩时间。minutes 为 0 时取消。暂停期间不能延长。
func (e *Engine) Extend(ctx context.Context, mac string, minutes int) (Snapshot, error) {
	if minutes < 0 || minutes > maxExtendMinutes {
		return Snapshot{}, inputf("临时延长要在 1 到 %d 分钟之间", maxExtendMinutes)
	}
	mac, err := normMAC(mac)
	if err != nil {
		return Snapshot{}, err
	}
	rows, err := e.db.Policies(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	var row *store.PolicyRecord
	for i := range rows {
		if rows[i].MAC == mac {
			row = &rows[i]
			break
		}
	}
	now := e.now()
	if minutes == 0 {
		if row == nil {
			return Snapshot{}, ErrNotFound
		}
		if err := e.db.SetExtend(ctx, mac, 0, now.Unix()); err != nil {
			return Snapshot{}, err
		}
		return e.Check(ctx)
	}
	if row == nil || !row.Enabled {
		return Snapshot{}, &InputError{Message: "请先启用这台设备的管控，并设置时长或时段"}
	}
	if row.Paused {
		return Snapshot{}, &InputError{Message: "设备已暂停上网，请先恢复再延长"}
	}
	spec, err := decodeSpec(row.Spec)
	if err != nil || !HasRestriction(spec) {
		return Snapshot{}, &InputError{Message: "当前没有时长或时段限制，不用延长"}
	}
	until := now.Add(time.Duration(minutes) * time.Minute).Unix()
	if err := e.db.SetExtend(ctx, mac, until, now.Unix()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, ErrNotFound
		}
		return Snapshot{}, err
	}
	return e.Check(ctx)
}

// Template 返回内嵌日历原文。
func (e *Engine) Template(year int) ([]byte, string, error) {
	return calendar.Template(year)
}

func (e *Engine) vacationStillUsable(ctx context.Context, v calendar.Vacations) error {
	rows, err := e.db.Policies(ctx)
	if err != nil {
		return err
	}
	needSummer, needWinter := false, false
	for _, row := range rows {
		spec, err := decodeSpec(row.Spec)
		if err != nil {
			continue
		}
		needSummer = needSummer || UsesKind(spec, DaySummer)
		needWinter = needWinter || UsesKind(spec, DayWinter)
	}
	if needSummer && v.Summer.Empty() {
		return &InputError{Message: "还有设备的策略在使用暑假，请先改掉这些策略再清空暑假"}
	}
	if needWinter && v.Winter.Empty() {
		return &InputError{Message: "还有设备的策略在使用寒假，请先改掉这些策略再清空寒假"}
	}
	return nil
}

func describe(y calendar.Year, vac calendar.Vacations, date string) DayInfo {
	d, ok := y.Get(date)
	if !ok {
		if fb, err := calendar.FallbackDay(date); err == nil {
			d = fb
		}
	}
	return DayInfo{
		Date: date, Weekday: d.Weekday, Workday: d.Workday, Name: d.Name, Kind: d.Kind,
		Summer: vac.Summer.Includes(date), Winter: vac.Winter.Includes(date),
	}
}

func calendarView(y calendar.Year, vac calendar.Vacations, today, stat DayInfo) CalendarView {
	view := CalendarView{
		Loaded: y.Official, Year: y.Meta.Year, Source: y.Meta.Source, Document: y.Meta.Document,
		Published: y.Meta.Published, SourceURL: y.Meta.SourceURL, ImportedAt: y.Meta.ImportedAt,
		Summer: vac.Summer, Winter: vac.Winter, Today: dayView(today), Stat: dayView(stat),
		Days: make([]DayView, 0, len(y.Days)),
	}
	for _, d := range y.Days {
		info := DayInfo{
			Date: d.Date, Weekday: d.Weekday, Workday: d.Workday, Name: d.Name, Kind: d.Kind,
			Summer: vac.Summer.Includes(d.Date), Winter: vac.Winter.Includes(d.Date),
		}
		switch d.Kind {
		case "off":
			view.OffDays++
		case "makeup":
			view.MakeupDays++
		}
		view.Days = append(view.Days, dayView(info))
	}
	return view
}

func classify(rules *category.Matcher, domains map[netip.Addr]string) (game, video []netip.Addr) {
	cats := rules.Categories()
	for ip, domain := range domains {
		idx, ok := rules.Match(domain)
		if !ok {
			idx, ok = rules.MatchIP(ip)
		}
		if !ok {
			continue
		}
		cat := cats[rules.App(idx).Category]
		switch cat.ID {
		case "game":
			game = append(game, ip)
		case "video", "shortvideo", "live":
			video = append(video, ip)
		}
	}
	return game, video
}

func decodeSpec(raw string) (Spec, error) {
	if raw == "" {
		return Spec{}, nil
	}
	var probe struct {
		DailyLimits []DailyLimit `json:"dailyLimits"`
		Windows     []Window     `json:"windows"`
		Schedule    *struct {
			Mode    string   `json:"mode"`
			Windows []Window `json:"windows"`
		} `json:"schedule"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return Spec{}, err
	}
	spec := Spec{DailyLimits: probe.DailyLimits, Windows: probe.Windows}
	if len(spec.Windows) == 0 && probe.Schedule != nil {
		for _, w := range probe.Schedule.Windows {
			switch probe.Schedule.Mode {
			case "block", "":
				w.BlockGame, w.BlockVideo = true, true
				spec.Windows = append(spec.Windows, w)
			case "allow":
				spec.Windows = append(spec.Windows, invertAllow(w)...)
			}
		}
	}
	return spec, nil
}

// invertAllow 把旧的「只在时段内允许娱乐」折成时段外禁止游戏和视频。
func invertAllow(w Window) []Window {
	start, end, overnight := bounds(w)
	w.BlockGame, w.BlockVideo = true, true
	if overnight {
		w.Start, w.End = formatClock(end), formatClock(start)
		return []Window{w}
	}
	var out []Window
	if start > 0 {
		gap := w
		gap.Start, gap.End = "00:00", formatClock(start)
		out = append(out, gap)
	}
	if end < 24*60 {
		gap := w
		gap.Start, gap.End = formatClock(end), "24:00"
		out = append(out, gap)
	}
	return out
}

func normMAC(s string) (string, error) {
	mac, ok := device.NormalizeMAC(s)
	if !ok {
		return "", &InputError{Message: "设备地址不正确"}
	}
	return mac, nil
}

func asInput(err error) error {
	if err == nil {
		return nil
	}
	var ie *InputError
	if errors.As(err, &ie) {
		return ie
	}
	var ce *calendar.Error
	if errors.As(err, &ce) {
		return &InputError{Message: ce.Error()}
	}
	return err
}

func mustYear(date string) int {
	t, err := calendar.ParseDate(date)
	if err != nil {
		return 0
	}
	return t.Year()
}
