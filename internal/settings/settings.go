package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/collector"
	"github.com/leo/leosentry/internal/config"
	"github.com/leo/leosentry/internal/policy"
	"github.com/leo/leosentry/internal/store"
)

const (
	keyCollect   = "collect_interval"
	keyPolicy    = "policy_interval"
	keyRotate    = "rotate_at"
	keyRules     = "category_edits"
	keyTLSCert   = "tls_cert"
	keyTLSKey    = "tls_key"
	keyHTTPPort  = "http_port"
	keyHTTPSPort = "https_port"
	keyMinFlow   = "min_flow_kb_per_min"
	maxSeconds   = 3600
	maxMinFlowKB = 1024
	maxAdded     = 400
)

// Error 是可以展示给用户的设置错误。
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Timing 是采集、策略检查和统计日切换。
type Timing struct {
	CollectSeconds int    `json:"collectSeconds"`
	PolicySeconds  int    `json:"policySeconds"`
	MinFlowKB      int    `json:"minFlowKB"`
	RotateAt       string `json:"rotateAt"`
	RotatePending  bool   `json:"rotatePending,omitempty"`
}

// minFlowHint 让概览页上的过滤说明跟着设置变。
type minFlowHint interface {
	SetMinFlowBytesPerMinute(int64)
}

// View 是系统设置页需要的数据。
type View struct {
	Timing
	TLS
	Categories []category.Group `json:"categories"`
}

// Service 读写会立刻影响采集和策略检查的设置，以及类别名单。
type Service struct {
	db             *store.PolicyStore
	live           *category.Live
	usage          *policy.Usage
	collector      *collector.Collector
	engine         *policy.Engine
	runningRotate  string
	tlsCert        string
	tlsKey         string
	httpPort       int
	tlsPort        int
	runningHTTP    int
	runningHTTPS   int
	tlsRunningCert string
	tlsRunningKey  string
	tlsError       string
	flowHint       minFlowHint
}

// Bind 把已经按启动配置跑起来的组件接上，便于之后修改立刻生效。
func Bind(db *store.PolicyStore, live *category.Live, usage *policy.Usage, col *collector.Collector, eng *policy.Engine, rotateAt string) *Service {
	return &Service{db: db, live: live, usage: usage, collector: col, engine: eng, runningRotate: rotateAt}
}

// ApplyStored 用数据库里保存的间隔和统计日覆盖启动配置。没有保存过时保持 cfg。
func ApplyStored(ctx context.Context, db *store.PolicyStore, cfg config.Config) (config.Config, error) {
	if v, ok, err := db.Setting(ctx, keyCollect); err != nil {
		return cfg, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxSeconds {
			cfg.CollectInterval = time.Duration(n) * time.Second
		}
	}
	if v, ok, err := db.Setting(ctx, keyPolicy); err != nil {
		return cfg, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxSeconds {
			cfg.PolicyInterval = time.Duration(n) * time.Second
		}
	}
	if v, ok, err := db.Setting(ctx, keyRotate); err != nil {
		return cfg, err
	} else if ok {
		if h, m, err := parseClock(v); err == nil {
			cfg.RotateHour, cfg.RotateMinute = h, m
		}
	}
	if v, ok, err := db.Setting(ctx, keyTLSCert); err != nil {
		return cfg, err
	} else if ok {
		cfg.TLSCertFile = strings.TrimSpace(v)
	}
	if v, ok, err := db.Setting(ctx, keyTLSKey); err != nil {
		return cfg, err
	} else if ok {
		cfg.TLSKeyFile = strings.TrimSpace(v)
	}
	if err := applyStoredPort(ctx, db, keyHTTPPort, &cfg.HTTPPort); err != nil {
		return cfg, err
	}
	if err := applyStoredPort(ctx, db, keyHTTPSPort, &cfg.HTTPSPort); err != nil {
		return cfg, err
	}
	if v, ok, err := db.Setting(ctx, keyMinFlow); err != nil {
		return cfg, err
	} else if ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 && n <= maxMinFlowKB {
			cfg.MinFlowBytesPerMinute = int64(n) << 10
		}
	}
	return cfg, nil
}

// UseMinFlowHint 让概览里的过滤说明使用页面上保存的阈值。
func (s *Service) UseMinFlowHint(h minFlowHint) {
	if s != nil {
		s.flowHint = h
	}
}

func applyStoredPort(ctx context.Context, db *store.PolicyStore, key string, dst *int) error {
	v, ok, err := db.Setting(ctx, key)
	if err != nil || !ok {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > 65535 {
		return nil
	}
	*dst = n
	return nil
}

// View 返回当前设置和各类别名单。
func (s *Service) View(ctx context.Context) (View, error) {
	edit, err := s.loadEdit(ctx)
	if err != nil {
		return View{}, err
	}
	groups, _, err := category.Catalog(edit)
	if err != nil {
		return View{}, err
	}
	collect := 60
	if s.collector != nil {
		collect = int(s.collector.Interval() / time.Second)
	} else if v, ok, err := s.db.Setting(ctx, keyCollect); err != nil {
		return View{}, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil {
			collect = n
		}
	} else if s.usage != nil {
		collect = int(s.usage.CurrentInterval() / time.Second)
	}
	policySec := 300
	if s.engine != nil {
		policySec = int(s.engine.CheckInterval() / time.Second)
	}
	rotate := fmt.Sprintf("%02d:%02d", 0, 0)
	if v, ok, err := s.db.Setting(ctx, keyRotate); err != nil {
		return View{}, err
	} else if ok && v != "" {
		rotate = v
	} else if s.runningRotate != "" {
		rotate = s.runningRotate
	}
	minKB := 8
	if s.collector != nil {
		minKB = int(s.collector.BytesPerMinute() >> 10)
	} else if v, ok, err := s.db.Setting(ctx, keyMinFlow); err != nil {
		return View{}, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil {
			minKB = n
		}
	}
	return View{
		Timing: Timing{
			CollectSeconds: collect,
			PolicySeconds:  policySec,
			MinFlowKB:      minKB,
			RotateAt:       rotate,
			RotatePending:  rotate != s.runningRotate,
		},
		TLS:        s.tlsView(),
		Categories: groups,
	}, nil
}

// SaveTiming 保存间隔、小流量门槛和统计日。间隔和门槛立刻生效，统计日在下次启动后生效。
func (s *Service) SaveTiming(ctx context.Context, collect, policySec, minFlowKB int, rotate string) (View, error) {
	if collect < 1 || collect > maxSeconds || policySec < 1 || policySec > maxSeconds {
		return View{}, &Error{Message: "间隔要在 1 到 3600 秒之间"}
	}
	if minFlowKB < 0 || minFlowKB > maxMinFlowKB {
		return View{}, &Error{Message: "忽略流量要在 0 到 1024 KB/分钟之间"}
	}
	if _, _, err := parseClock(rotate); err != nil {
		return View{}, &Error{Message: "统计日切换时间不正确，请用 03:00 这种格式"}
	}
	if err := s.db.SetSetting(ctx, keyCollect, strconv.Itoa(collect)); err != nil {
		return View{}, err
	}
	if err := s.db.SetSetting(ctx, keyPolicy, strconv.Itoa(policySec)); err != nil {
		return View{}, err
	}
	if err := s.db.SetSetting(ctx, keyMinFlow, strconv.Itoa(minFlowKB)); err != nil {
		return View{}, err
	}
	if err := s.db.SetSetting(ctx, keyRotate, rotate); err != nil {
		return View{}, err
	}
	d := time.Duration(collect) * time.Second
	perMin := int64(minFlowKB) << 10
	if s.collector != nil {
		s.collector.SetMinBytesPerMinute(perMin)
		s.collector.SetInterval(d)
	}
	if s.usage != nil {
		s.usage.SetInterval(d)
	}
	if s.engine != nil {
		s.engine.SetCheckInterval(time.Duration(policySec) * time.Second)
	}
	if s.flowHint != nil {
		s.flowHint.SetMinFlowBytesPerMinute(perMin)
	}
	return s.View(ctx)
}

// AddRule 往类别里加一条网站、通配或 IPv4。
func (s *Service) AddRule(ctx context.Context, catID, raw string) (View, error) {
	kind, value, err := category.ParseValue(raw)
	if err != nil || kind == "" {
		return View{}, &Error{Message: "请填写域名、带 * 的通配，或一个 IPv4 地址"}
	}
	edit, err := s.loadEdit(ctx)
	if err != nil {
		return View{}, err
	}
	kept := edit.Removed[:0]
	wasRemoved := false
	for _, v := range edit.Removed {
		if v == value {
			wasRemoved = true
			continue
		}
		kept = append(kept, v)
	}
	edit.Removed = kept
	if !wasRemoved {
		current, _, err := category.Catalog(edit)
		if err != nil {
			return View{}, err
		}
		for _, g := range current {
			for _, entry := range g.Entries {
				if entry.Value == value {
					return View{}, &Error{Message: "这条已经在名单里"}
				}
			}
		}
		if len(edit.Added) >= maxAdded {
			return View{}, &Error{Message: "添加的条目太多了"}
		}
		edit.Added = append(edit.Added, category.Added{Category: catID, Value: value})
	}
	return s.commitRules(ctx, edit)
}

// RemoveRule 从类别里去掉一条。内置条目删掉后可以再加回来。
func (s *Service) RemoveRule(ctx context.Context, catID, raw string) (View, error) {
	_, value, err := category.ParseValue(raw)
	if err != nil {
		value = raw
	}
	edit, err := s.loadEdit(ctx)
	if err != nil {
		return View{}, err
	}
	var added []category.Added
	removedCustom := false
	for _, a := range edit.Added {
		if a.Category == catID && a.Value == value {
			removedCustom = true
			continue
		}
		added = append(added, a)
	}
	edit.Added = added
	if !removedCustom {
		found := false
		for _, v := range edit.Removed {
			if v == value {
				found = true
				break
			}
		}
		if !found {
			edit.Removed = append(edit.Removed, value)
		}
	}
	return s.commitRules(ctx, edit)
}

func (s *Service) commitRules(ctx context.Context, edit category.Edit) (View, error) {
	groups, matcher, err := category.Catalog(edit)
	if err != nil {
		return View{}, &Error{Message: "这条没能加进名单，可能已经在别的类别里，或类别不存在"}
	}
	body, err := json.Marshal(edit)
	if err != nil {
		return View{}, err
	}
	if err := s.db.SetSetting(ctx, keyRules, string(body)); err != nil {
		return View{}, err
	}
	if s.live != nil {
		s.live.Set(matcher)
	}
	if s.usage != nil {
		s.usage.SetRules(matcher)
	}
	view, err := s.View(ctx)
	if err != nil {
		return View{}, err
	}
	view.Categories = groups
	return view, nil
}

func (s *Service) loadEdit(ctx context.Context) (category.Edit, error) {
	raw, ok, err := s.db.Setting(ctx, keyRules)
	if err != nil || !ok || raw == "" {
		return category.Edit{}, err
	}
	var edit category.Edit
	if err := json.Unmarshal([]byte(raw), &edit); err != nil {
		return category.Edit{}, nil
	}
	return edit, nil
}

// LoadRules 按已保存的增删编译规则。没有改过时就是内置规则。
func LoadRules(ctx context.Context, db *store.PolicyStore) (*category.Matcher, error) {
	var edit category.Edit
	if raw, ok, err := db.Setting(ctx, keyRules); err != nil {
		return nil, err
	} else if ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &edit)
	}
	_, matcher, err := category.Catalog(edit)
	return matcher, err
}

func parseClock(v string) (int, int, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}
