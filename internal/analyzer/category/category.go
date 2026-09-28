package category

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"net/netip"

	"github.com/leo/leosentry/assets"
)

func fsSubRules() (fs.FS, error) {
	return fs.Sub(assets.Rules, "rules")
}

// 必须存在的两个兜底类别：有域名但规则库未收录的归入 Other，没有域名的归入 Unknown。
const (
	Other   = "other"
	Unknown = "unknown"
)

// Category 是一个行为类别。Entertainment 为 true 的类别计入"娱乐时长"。
type Category struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Entertainment bool   `json:"entertainment,omitempty"`
}

// App 是规则库中的一个应用，Category 为其在 Categories() 中的下标。
type App struct {
	Name     string
	Category int
}

type ruleFile struct {
	Categories []Category `json:"categories"`
	Apps       []struct {
		Name     string   `json:"name"`
		Category string   `json:"category"`
		Domains  []string `json:"domains"`
		Patterns []string `json:"patterns"`
	} `json:"apps"`
	DeviceHints []struct {
		Name     string   `json:"name"`
		Domains  []string `json:"domains"`
		Patterns []string `json:"patterns"`
	} `json:"deviceHints"`
}

type pattern struct {
	re  *regexp.Regexp
	idx int
}

// Matcher 按域名识别应用、类别与设备类型线索。构建后只读，可并发使用。
type Matcher struct {
	categories []Category
	catIndex   map[string]int
	apps       []App

	appSuffix    map[string]int
	appPatterns  []pattern
	ips          map[netip.Addr]int
	hints        []string
	hintSuffix   map[string]int
	hintPatterns []pattern
}

// Default 加载内嵌的规则库。
func Default() (*Matcher, error) {
	sub, err := fs.Sub(assets.Rules, "rules")
	if err != nil {
		return nil, err
	}
	return Load(sub)
}

// Load 读取 fsys 中全部 *.json 规则文件（按路径排序）。
// domains 按后缀匹配（"qq.com" 匹配自身及所有子域名，最长后缀优先），
// patterns 为通配规则（"*" 匹配任意字符），优先于 domains。
func Load(fsys fs.FS) (*Matcher, error) {
	parsed, err := readDocs(fsys)
	if err != nil {
		return nil, err
	}
	return compile(parsed)
}

func readDocs(fsys fs.FS) ([]ruleFile, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path.Ext(p) == ".json" {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	parsed := make([]ruleFile, len(files))
	for i, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &parsed[i]); err != nil {
			return nil, fmt.Errorf("rules %s: %w", f, err)
		}
	}
	return parsed, nil
}

func compile(parsed []ruleFile) (*Matcher, error) {
	m := &Matcher{
		catIndex:   make(map[string]int),
		appSuffix:  make(map[string]int),
		ips:        make(map[netip.Addr]int),
		hintSuffix: make(map[string]int),
	}
	for _, rf := range parsed {
		for _, c := range rf.Categories {
			if _, dup := m.catIndex[c.ID]; dup {
				return nil, fmt.Errorf("rules: duplicate category %q", c.ID)
			}
			m.catIndex[c.ID] = len(m.categories)
			m.categories = append(m.categories, c)
		}
	}
	for _, id := range []string{Other, Unknown} {
		if _, ok := m.catIndex[id]; !ok {
			return nil, fmt.Errorf("rules: category %q is required", id)
		}
	}

	var errs []error
	for _, rf := range parsed {
		for _, a := range rf.Apps {
			ci, ok := m.catIndex[a.Category]
			if !ok {
				errs = append(errs, fmt.Errorf("app %s: unknown category %q", a.Name, a.Category))
				continue
			}
			idx := len(m.apps)
			m.apps = append(m.apps, App{Name: a.Name, Category: ci})
			errs = append(errs, addIPs(m.ips, idx, a.Name, a.Domains))
			errs = append(errs, addRules(m.appSuffix, &m.appPatterns, idx, a.Name, a.Domains, a.Patterns))
		}
		for _, h := range rf.DeviceHints {
			idx := len(m.hints)
			m.hints = append(m.hints, h.Name)
			errs = append(errs, addRules(m.hintSuffix, &m.hintPatterns, idx, h.Name, h.Domains, h.Patterns))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	return m, nil
}

func addIPs(dst map[netip.Addr]int, idx int, owner string, domains []string) error {
	var errs []error
	for _, d := range domains {
		ip, err := netip.ParseAddr(Normalize(d))
		if err != nil || !ip.Is4() {
			continue
		}
		if _, dup := dst[ip]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate ip %s", owner, ip))
			continue
		}
		dst[ip] = idx
	}
	return errors.Join(errs...)
}

func addRules(suffix map[string]int, patterns *[]pattern, idx int, owner string, domains, globs []string) error {
	var errs []error
	for _, d := range domains {
		d = Normalize(d)
		if ip, err := netip.ParseAddr(d); err == nil && ip.Is4() {
			continue
		}
		if _, dup := suffix[d]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate domain %q", owner, d))
			continue
		}
		suffix[d] = idx
	}
	for _, g := range globs {
		expr := "^" + strings.ReplaceAll(regexp.QuoteMeta(Normalize(g)), `\*`, ".*") + "$"
		re, err := regexp.Compile(expr)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: pattern %q: %w", owner, g, err))
			continue
		}
		*patterns = append(*patterns, pattern{re: re, idx: idx})
	}
	return errors.Join(errs...)
}

// Categories 返回全部类别，顺序即规则文件中的定义顺序。
func (m *Matcher) Categories() []Category { return m.categories }

// CategoryIndex 返回类别 id 的下标，不存在时返回 -1。
func (m *Matcher) CategoryIndex(id string) int {
	if i, ok := m.catIndex[id]; ok {
		return i
	}
	return -1
}

// App 返回规则库中下标为 i 的应用。
func (m *Matcher) App(i int) App { return m.apps[i] }

// Get 返回当前规则。*Matcher 本身就是一份固定规则，便于和可热更新的 Live 用同一接口。
func (m *Matcher) Get() *Matcher { return m }

// Match 返回域名所属应用在规则库中的下标。
func (m *Matcher) Match(domain string) (int, bool) {
	if m == nil {
		return 0, false
	}
	return lookup(m.appSuffix, m.appPatterns, Normalize(domain))
}

// MatchIP 返回 IPv4 地址所属应用在规则库中的下标。
func (m *Matcher) MatchIP(ip netip.Addr) (int, bool) {
	if m == nil || !ip.Is4() {
		return 0, false
	}
	i, ok := m.ips[ip.Unmap()]
	return i, ok
}

// Hint 返回域名暗示的设备类型（如"苹果设备"）。
func (m *Matcher) Hint(domain string) (string, bool) {
	if i, ok := lookup(m.hintSuffix, m.hintPatterns, Normalize(domain)); ok {
		return m.hints[i], true
	}
	return "", false
}

func lookup(suffix map[string]int, patterns []pattern, d string) (int, bool) {
	if d == "" {
		return 0, false
	}
	for _, p := range patterns {
		if p.re.MatchString(d) {
			return p.idx, true
		}
	}
	for s := d; ; {
		if i, ok := suffix[s]; ok {
			return i, true
		}
		dot := strings.IndexByte(s, '.')
		if dot < 0 {
			return 0, false
		}
		s = s[dot+1:]
	}
}

// Normalize 把域名转为小写并去掉末尾的点。
func Normalize(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

// secondLevel 是国家顶级域名下常见的二级分类，"example.com.cn" 的主域名取三段。
var secondLevel = map[string]bool{"com": true, "net": true, "org": true, "gov": true, "edu": true, "ac": true, "co": true}

// RegistrableDomain 近似求主域名（如 "a.b.example.com" → "example.com"、
// "x.example.com.cn" → "example.com.cn"），用于把规则库未收录的域名按网站归并。
func RegistrableDomain(domain string) string {
	d := Normalize(domain)
	labels := strings.Split(d, ".")
	n := len(labels)
	if n <= 2 {
		return d
	}
	if len(labels[n-1]) == 2 && secondLevel[labels[n-2]] {
		return strings.Join(labels[n-3:], ".")
	}
	return strings.Join(labels[n-2:], ".")
}
