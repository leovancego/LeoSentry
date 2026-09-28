package policy

import (
	"slices"
	"time"

	"github.com/leo/leosentry/internal/policy/calendar"
)

// Snapshot 是最近一次策略检查的结果，不含设备名称。
type Snapshot struct {
	CheckedAt    time.Time
	TZOffset     int
	StatDay      string
	CivilToday   string
	Firewall     bool
	EnforceError string
	Calendar     CalendarView
	ByMAC        map[string]Decision
}

// CalendarView 是页面上的年度日历。
type CalendarView struct {
	Loaded     bool          `json:"loaded"`
	Year       int           `json:"year"`
	Source     string        `json:"source,omitempty"`
	Document   string        `json:"document,omitempty"`
	Published  string        `json:"published,omitempty"`
	SourceURL  string        `json:"sourceURL,omitempty"`
	ImportedAt int64         `json:"importedAt,omitempty"`
	OffDays    int           `json:"offDays"`
	MakeupDays int           `json:"makeupDays"`
	Summer     calendar.Span `json:"summer"`
	Winter     calendar.Span `json:"winter"`
	Today      DayView       `json:"today"`
	Stat       DayView       `json:"stat"`
	Days       []DayView     `json:"days"`
}

// DayView 是日历上的一天。
type DayView struct {
	Date    string `json:"date"`
	Weekday int    `json:"weekday"`
	Workday bool   `json:"workday"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Summer  bool   `json:"summer,omitempty"`
	Winter  bool   `json:"winter,omitempty"`
}

// DeviceInfo 是页面合并用的设备身份，由设备目录提供。
type DeviceInfo struct {
	MAC      string
	Name     string
	Hostname string
	IP       string
	Online   bool
}

// DeviceView 是管控页上一台设备的策略、用量和当前判定。
type DeviceView struct {
	MAC             string       `json:"mac"`
	Name            string       `json:"name,omitempty"`
	Hostname        string       `json:"hostname,omitempty"`
	IP              string       `json:"ip,omitempty"`
	Online          bool         `json:"online"`
	Enabled         bool         `json:"enabled"`
	Paused          bool         `json:"paused"`
	ExtendUntil     int64        `json:"extendUntil,omitempty"`
	Controlled      bool         `json:"controlled"`
	DailyLimits     []DailyLimit `json:"dailyLimits,omitempty"`
	Windows         []Window     `json:"windows,omitempty"`
	InternetMinutes int          `json:"internetMinutes"`
	GameMinutes     int          `json:"gameMinutes"`
	VideoMinutes    int          `json:"videoMinutes"`
	InternetLimit   int          `json:"internetLimit"`
	GameLimit       int          `json:"gameLimit"`
	VideoLimit      int          `json:"videoLimit"`
	LimitLabel      string       `json:"limitLabel,omitempty"`
	Action          string       `json:"action"`
	BlockGame       bool         `json:"blockGame,omitempty"`
	BlockVideo      bool         `json:"blockVideo,omitempty"`
	Reason          string       `json:"reason"`
	Text            string       `json:"text"`
}

// Page 是 GET /api/v1/policies 的响应。
type Page struct {
	GeneratedAt  int64        `json:"generatedAt"`
	CheckedAt    int64        `json:"checkedAt"`
	TZOffset     int          `json:"tzOffset"`
	StatDay      string       `json:"statDay"`
	CivilToday   string       `json:"civilToday"`
	Firewall     bool         `json:"firewall"`
	EnforceError string       `json:"enforceError,omitempty"`
	Calendar     CalendarView `json:"calendar"`
	Devices      []DeviceView `json:"devices"`
	Templates    []Template   `json:"templates,omitempty"`
}

// Template 是可套用到设备上的一份策略，改模板不会自动改已经套用过的设备。
type Template struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	DailyLimits []DailyLimit `json:"dailyLimits,omitempty"`
	Windows     []Window     `json:"windows,omitempty"`
}

// Present 把最近一次判定和当前设备列表合成页面数据。
func Present(s Snapshot, known []DeviceInfo) Page {
	page := Page{
		GeneratedAt:  time.Now().Unix(),
		TZOffset:     s.TZOffset,
		StatDay:      s.StatDay,
		CivilToday:   s.CivilToday,
		Firewall:     s.Firewall,
		EnforceError: s.EnforceError,
		Calendar:     s.Calendar,
		Devices:      make([]DeviceView, 0, len(known)+len(s.ByMAC)),
	}
	if !s.CheckedAt.IsZero() {
		page.CheckedAt = s.CheckedAt.Unix()
	}
	seen := make(map[string]bool, len(known))
	for _, d := range known {
		if d.MAC == "" {
			continue
		}
		seen[d.MAC] = true
		page.Devices = append(page.Devices, deviceView(d, s.ByMAC[d.MAC]))
	}
	extra := make([]string, 0)
	for mac := range s.ByMAC {
		if !seen[mac] {
			extra = append(extra, mac)
		}
	}
	slices.Sort(extra)
	for _, mac := range extra {
		page.Devices = append(page.Devices, deviceView(DeviceInfo{MAC: mac}, s.ByMAC[mac]))
	}
	return page
}

func deviceView(info DeviceInfo, dec Decision) DeviceView {
	if dec.Action == "" {
		dec = Decision{Action: ActionAllow, Reason: ReasonOff, Text: "未启用管控"}
	}
	return DeviceView{
		MAC: info.MAC, Name: info.Name, Hostname: info.Hostname, IP: info.IP, Online: info.Online,
		Enabled: dec.Enabled, Paused: dec.Paused, ExtendUntil: dec.ExtendUntil, Controlled: dec.Controlled,
		DailyLimits: dec.Spec.DailyLimits, Windows: dec.Spec.Windows,
		InternetMinutes: dec.Usage.Internet, GameMinutes: dec.Usage.Game, VideoMinutes: dec.Usage.Video,
		InternetLimit: dec.InternetLimit, GameLimit: dec.GameLimit, VideoLimit: dec.VideoLimit, LimitLabel: dec.LimitLabel,
		Action: dec.Action, BlockGame: dec.BlockGame, BlockVideo: dec.BlockVideo, Reason: dec.Reason, Text: dec.Text,
	}
}

func dayView(d DayInfo) DayView {
	return DayView{
		Date: d.Date, Weekday: d.Weekday, Workday: d.Workday, Name: d.Name, Kind: d.Kind,
		Summer: d.Summer, Winter: d.Winter,
	}
}
