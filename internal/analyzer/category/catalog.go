package category

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// Edit 是相对内置规则的增删。Removed 里的值不再使用内置类别；
// 若同一个值也出现在 Added 中，且类别不同，则改挂到 Added 指定的类别。
type Edit struct {
	Removed []string
	Added   []Added
}

// Added 是用户加到某个类别上的一条网站、通配或 IPv4。
type Added struct {
	Category string `json:"category"`
	Value    string `json:"value"`
}

// Entry 是某个类别下的一条名单。
type Entry struct {
	Name    string `json:"name,omitempty"`
	Value   string `json:"value"`
	Kind    string `json:"kind"`
	Builtin bool   `json:"builtin,omitempty"`
}

// Group 是一个类别及其名单。
type Group struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Entries []Entry `json:"entries"`
}

const (
	KindDomain  = "domain"
	KindPattern = "pattern"
	KindIP      = "ip"
)

var (
	baseOnce sync.Once
	baseDocs []ruleFile
	baseErr  error
)

// Base 返回内置规则的副本。
func Base() ([]ruleFile, error) {
	baseOnce.Do(func() {
		sub, err := fsSubRules()
		if err != nil {
			baseErr = err
			return
		}
		baseDocs, baseErr = readDocs(sub)
	})
	if baseErr != nil {
		return nil, baseErr
	}
	out := make([]ruleFile, len(baseDocs))
	for i, doc := range baseDocs {
		out[i] = doc
		out[i].Categories = slices.Clone(doc.Categories)
		out[i].Apps = slices.Clone(doc.Apps)
		out[i].DeviceHints = slices.Clone(doc.DeviceHints)
		for j := range out[i].Apps {
			out[i].Apps[j].Domains = slices.Clone(doc.Apps[j].Domains)
			out[i].Apps[j].Patterns = slices.Clone(doc.Apps[j].Patterns)
		}
	}
	return out, nil
}

// ParseValue 判断一条输入是域名、通配还是 IPv4。
func ParseValue(raw string) (kind, value string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t/") || utf8.RuneCountInString(s) > 253 {
		return "", "", fmt.Errorf("bad")
	}
	if ip, err := netip.ParseAddr(s); err == nil && ip.Is4() {
		return KindIP, ip.String(), nil
	}
	s = Normalize(s)
	if strings.Contains(s, "*") {
		if !validPattern(s) {
			return "", "", fmt.Errorf("bad")
		}
		return KindPattern, s, nil
	}
	if !validDomain(s) {
		return "", "", fmt.Errorf("bad")
	}
	return KindDomain, s, nil
}

// Catalog 按当前增删生成各类别名单，并编译出可匹配的规则。
func Catalog(edit Edit) ([]Group, *Matcher, error) {
	docs, err := Base()
	if err != nil {
		return nil, nil, err
	}
	removed := map[string]struct{}{}
	for _, v := range edit.Removed {
		removed[v] = struct{}{}
	}
	builtin := map[string]string{}
	var groups []Group
	order := map[string]int{}
	for _, doc := range docs {
		for _, c := range doc.Categories {
			order[c.ID] = len(groups)
			groups = append(groups, Group{ID: c.ID, Name: c.Name})
		}
	}
	for di := range docs {
		for ai := range docs[di].Apps {
			app := &docs[di].Apps[ai]
			gi, ok := order[app.Category]
			if !ok {
				continue
			}
			var domains []string
			for _, d := range app.Domains {
				kind, value, err := ParseValue(d)
				if err != nil {
					domains = append(domains, d)
					continue
				}
				builtin[value] = app.Category
				if _, drop := removed[value]; drop {
					continue
				}
				domains = append(domains, value)
				groups[gi].Entries = append(groups[gi].Entries, Entry{Name: app.Name, Value: value, Kind: kind, Builtin: true})
			}
			var patterns []string
			for _, p := range app.Patterns {
				_, value, err := ParseValue(p)
				if err != nil {
					patterns = append(patterns, p)
					continue
				}
				builtin[value] = app.Category
				if _, drop := removed[value]; drop {
					continue
				}
				patterns = append(patterns, value)
				groups[gi].Entries = append(groups[gi].Entries, Entry{Name: app.Name, Value: value, Kind: KindPattern, Builtin: true})
			}
			app.Domains = domains
			app.Patterns = patterns
		}
	}
	var extra ruleFile
	seenAdded := map[string]struct{}{}
	for _, item := range edit.Added {
		if _, drop := removed[item.Value]; drop {
			if bcat, isBuiltin := builtin[item.Value]; !isBuiltin || bcat == item.Category {
				continue
			}
		}
		gi, ok := order[item.Category]
		if !ok {
			return nil, nil, fmt.Errorf("unknown category")
		}
		kind, value, err := ParseValue(item.Value)
		if err != nil {
			return nil, nil, fmt.Errorf("bad value")
		}
		if cat, ok := builtin[value]; ok && cat == item.Category {
			continue
		}
		if _, dup := seenAdded[value]; dup {
			continue
		}
		seenAdded[value] = struct{}{}
		groups[gi].Entries = append(groups[gi].Entries, Entry{Name: value, Value: value, Kind: kind})
		row := struct {
			Name     string   `json:"name"`
			Category string   `json:"category"`
			Domains  []string `json:"domains"`
			Patterns []string `json:"patterns"`
		}{Name: value, Category: item.Category}
		if kind == KindPattern {
			row.Patterns = []string{value}
		} else {
			row.Domains = []string{value}
		}
		extra.Apps = append(extra.Apps, row)
	}
	if len(extra.Apps) > 0 {
		docs = append(docs, extra)
	}
	for i := range groups {
		slices.SortFunc(groups[i].Entries, func(a, b Entry) int {
			if c := strings.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			return strings.Compare(a.Value, b.Value)
		})
	}
	m, err := compile(docs)
	if err != nil {
		return nil, nil, err
	}
	return groups, m, nil
}

func validDomain(s string) bool {
	if !strings.Contains(s, ".") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func validPattern(s string) bool {
	if !strings.Contains(s, ".") || strings.Contains(s, "**") {
		return false
	}
	plain := strings.ReplaceAll(s, "*", "a")
	return validDomain(plain)
}
