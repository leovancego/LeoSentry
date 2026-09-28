package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/leo/leosentry/internal/analyzer/category"
)

// categoryDocument 是类别名单的导入导出文件。
type categoryDocument struct {
	Categories []categoryDocumentGroup `json:"categories"`
}

type categoryDocumentGroup struct {
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	Entries []categoryDocumentEntry `json:"entries"`
}

type categoryDocumentEntry struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value"`
}

// ExportCategories 把当前全部类别名单写成 JSON 文件。
func (s *Service) ExportCategories(ctx context.Context) ([]byte, error) {
	view, err := s.View(ctx)
	if err != nil {
		return nil, err
	}
	return marshalCategories(view.Categories)
}

func marshalCategories(groups []category.Group) ([]byte, error) {
	doc := categoryDocument{Categories: make([]categoryDocumentGroup, 0, len(groups))}
	for _, g := range groups {
		entries := make([]categoryDocumentEntry, 0, len(g.Entries))
		for _, e := range g.Entries {
			item := categoryDocumentEntry{Value: e.Value}
			if e.Name != "" && e.Name != e.Value {
				item.Name = e.Name
			}
			entries = append(entries, item)
		}
		doc.Categories = append(doc.Categories, categoryDocumentGroup{
			ID: g.ID, Name: g.Name, Entries: entries,
		})
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// ImportCategories 用文件里的名单换掉当前全部类别。文件格式、域名和 IPv4 不合法时不会改动现有名单。
func (s *Service) ImportCategories(ctx context.Context, data []byte) (View, error) {
	base, _, err := category.Catalog(category.Edit{})
	if err != nil {
		return View{}, err
	}
	edit, err := editFromDocument(data, base)
	if err != nil {
		return View{}, err
	}
	return s.commitRules(ctx, edit)
}

func editFromDocument(data []byte, base []category.Group) (category.Edit, error) {
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}))
	dec := json.NewDecoder(bytes.NewReader(data))
	var root map[string]json.RawMessage
	if err := dec.Decode(&root); err != nil {
		return category.Edit{}, &Error{Message: "文件格式不正确"}
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return category.Edit{}, &Error{Message: "文件格式不正确"}
	}
	rawCats, ok := root["categories"]
	if !ok || len(root) != 1 || !isJSONArray(rawCats) {
		return category.Edit{}, &Error{Message: "文件格式不正确"}
	}
	var cats []json.RawMessage
	if err := json.Unmarshal(rawCats, &cats); err != nil {
		return category.Edit{}, &Error{Message: "文件格式不正确"}
	}

	known := make(map[string]string, len(base))
	order := make([]string, 0, len(base))
	builtin := map[string]string{}
	for _, g := range base {
		known[g.ID] = g.Name
		order = append(order, g.ID)
		for _, e := range g.Entries {
			builtin[e.Value] = g.ID
		}
	}

	seenCat := map[string]struct{}{}
	desired := map[string]string{}
	var added []category.Added
	for _, raw := range cats {
		id, entries, err := decodeCategory(raw, known)
		if err != nil {
			return category.Edit{}, err
		}
		display := known[id]
		if _, dup := seenCat[id]; dup {
			return category.Edit{}, &Error{Message: "类别「" + display + "」重复了"}
		}
		seenCat[id] = struct{}{}
		for _, rawEntry := range entries {
			name, value, err := decodeEntry(display, rawEntry)
			if err != nil {
				return category.Edit{}, err
			}
			norm, vErr := normalizeValue(value)
			if vErr != nil {
				return category.Edit{}, vErr
			}
			name, err = cleanName(name, norm)
			if err != nil {
				return category.Edit{}, err
			}
			if prev, dup := desired[norm]; dup {
				if prev == id {
					return category.Edit{}, &Error{Message: norm + " 在「" + display + "」里重复了"}
				}
				return category.Edit{}, &Error{Message: norm + " 不能同时出现在「" + known[prev] + "」和「" + display + "」"}
			}
			desired[norm] = id
			if builtin[norm] == id {
				continue
			}
			added = append(added, category.Added{Category: id, Value: norm, Name: name})
		}
	}

	var missing []string
	for _, id := range order {
		if _, ok := seenCat[id]; !ok {
			missing = append(missing, known[id])
		}
	}
	if len(missing) > 0 {
		return category.Edit{}, &Error{Message: "缺少类别：" + strings.Join(missing, "、")}
	}
	if len(added) > maxAdded {
		return category.Edit{}, &Error{Message: "添加的条目太多了"}
	}

	var removed []string
	for value, bcat := range builtin {
		if desired[value] != bcat {
			removed = append(removed, value)
		}
	}
	slices.Sort(removed)
	return category.Edit{Removed: removed, Added: added}, nil
}

func decodeCategory(raw json.RawMessage, known map[string]string) (string, []json.RawMessage, error) {
	if !isJSONObject(raw) {
		return "", nil, &Error{Message: "文件格式不正确"}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", nil, &Error{Message: "文件格式不正确"}
	}
	for k := range obj {
		if k != "id" && k != "name" && k != "entries" {
			return "", nil, &Error{Message: "文件格式不正确"}
		}
	}
	rawID, ok := obj["id"]
	if !ok {
		return "", nil, &Error{Message: "有一个类别没有 id"}
	}
	var id string
	if json.Unmarshal(rawID, &id) != nil {
		return "", nil, &Error{Message: "文件格式不正确"}
	}
	if strings.TrimSpace(id) == "" {
		return "", nil, &Error{Message: "有一个类别没有 id"}
	}
	if rawName, ok := obj["name"]; ok {
		var name string
		if json.Unmarshal(rawName, &name) != nil {
			return "", nil, &Error{Message: "文件格式不正确"}
		}
	}
	display, knownID := known[id]
	if !knownID {
		return "", nil, &Error{Message: "没有这个类别：" + clip(id)}
	}
	rawEntries, ok := obj["entries"]
	var entries []json.RawMessage
	if !ok || !isJSONArray(rawEntries) || json.Unmarshal(rawEntries, &entries) != nil {
		return "", nil, &Error{Message: "类别「" + display + "」的名单格式不正确"}
	}
	return id, entries, nil
}

func decodeEntry(catName string, raw json.RawMessage) (name, value string, err error) {
	if !isJSONObject(raw) {
		return "", "", &Error{Message: "类别「" + catName + "」的名单格式不正确"}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", "", &Error{Message: "类别「" + catName + "」的名单格式不正确"}
	}
	for k := range obj {
		if k != "name" && k != "value" {
			return "", "", &Error{Message: "类别「" + catName + "」的名单格式不正确"}
		}
	}
	rawValue, ok := obj["value"]
	if !ok {
		return "", "", &Error{Message: "类别「" + catName + "」有一条名单没有填写网站或 IP"}
	}
	if json.Unmarshal(rawValue, &value) != nil {
		return "", "", &Error{Message: "类别「" + catName + "」的名单格式不正确"}
	}
	if rawName, ok := obj["name"]; ok {
		if json.Unmarshal(rawName, &name) != nil {
			return "", "", &Error{Message: "类别「" + catName + "」的名单格式不正确"}
		}
	}
	if strings.TrimSpace(value) == "" {
		return "", "", &Error{Message: "类别「" + catName + "」有一条名单没有填写网站或 IP"}
	}
	return name, value, nil
}

func cleanName(name, value string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == value {
		return "", nil
	}
	if utf8.RuneCountInString(name) > 40 {
		return "", &Error{Message: "名称太长：" + clip(name)}
	}
	return name, nil
}

func normalizeValue(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	shown := clip(s)
	if ipv4Shape(s) || strings.Contains(s, ":") {
		ip, err := netip.ParseAddr(s)
		if err != nil || !ip.Is4() {
			return "", &Error{Message: "IP 地址格式不正确：" + shown}
		}
		return ip.String(), nil
	}
	kind, value, err := category.ParseValue(s)
	if err != nil || kind == "" {
		return "", &Error{Message: "域名格式不正确：" + shown}
	}
	return value, nil
}

func ipv4Shape(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func isJSONArray(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '['
}

func isJSONObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) <= 80 {
		return s
	}
	return string([]rune(s)[:80]) + "…"
}
