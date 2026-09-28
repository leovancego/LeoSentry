package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/store"
)

func openSettings(t *testing.T) (*Service, *category.Live) {
	t.Helper()
	db, err := store.OpenPolicy(context.Background(), filepath.Join(t.TempDir(), "policy.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, matcher, err := category.Catalog(category.Edit{})
	if err != nil {
		t.Fatal(err)
	}
	live := category.NewLive(matcher)
	return Bind(db, live, nil, nil, nil, "03:00"), live
}

func entryValues(groups []category.Group) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(groups))
	for _, g := range groups {
		set := make(map[string]bool, len(g.Entries))
		for _, e := range g.Entries {
			set[e.Value] = true
		}
		out[g.ID] = set
	}
	return out
}

func sameValues(a, b []category.Group) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := entryValues(a), entryValues(b)
	for id, set := range left {
		if len(set) != len(right[id]) {
			return false
		}
		for v := range set {
			if !right[id][v] {
				return false
			}
		}
	}
	return true
}

func groupByID(groups []category.Group, id string) category.Group {
	for _, g := range groups {
		if g.ID == id {
			return g
		}
	}
	return category.Group{}
}

func docGroup(doc *categoryDocument, id string) *categoryDocumentGroup {
	for i := range doc.Categories {
		if doc.Categories[i].ID == id {
			return &doc.Categories[i]
		}
	}
	return nil
}

func without(entries []categoryDocumentEntry, value string) []categoryDocumentEntry {
	var kept []categoryDocumentEntry
	for _, e := range entries {
		if e.Value != value {
			kept = append(kept, e)
		}
	}
	return kept
}

func cloneDoc(doc categoryDocument) categoryDocument {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	var out categoryDocument
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func withValue(t *testing.T, doc categoryDocument, id, value string) []byte {
	t.Helper()
	next := cloneDoc(doc)
	g := docGroup(&next, id)
	g.Entries = append(g.Entries, categoryDocumentEntry{Value: value})
	return mustJSON(t, next)
}

func builtinDoc(t *testing.T) categoryDocument {
	t.Helper()
	groups, _, err := category.Catalog(category.Edit{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := marshalCategories(groups)
	if err != nil {
		t.Fatal(err)
	}
	var doc categoryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestExportRoundTripKeepsBuiltinNames(t *testing.T) {
	svc, _ := openSettings(t)
	ctx := context.Background()
	before, err := svc.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, err := svc.ExportCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddRule(ctx, "game", "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveRule(ctx, "music", "kugou.com"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.ImportCategories(ctx, append([]byte{0xEF, 0xBB, 0xBF}, body...))
	if err != nil {
		t.Fatal(err)
	}
	if !sameValues(before.Categories, after.Categories) {
		t.Fatal("import of an export should restore the exported lists")
	}
	var named bool
	for _, e := range groupByID(after.Categories, "video").Entries {
		if e.Value == "v.qq.com" && e.Name == "腾讯视频" {
			named = true
		}
	}
	if !named {
		t.Fatal("builtin name was not restored")
	}
}

func TestImportReplacesLists(t *testing.T) {
	svc, live := openSettings(t)
	ctx := context.Background()
	if _, err := svc.AddRule(ctx, "game", "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveRule(ctx, "music", "kugou.com"); err != nil {
		t.Fatal(err)
	}
	doc := builtinDoc(t)
	game := docGroup(&doc, "game")
	game.Entries = without(game.Entries, "smoba.qq.com")
	video := docGroup(&doc, "video")
	video.Entries = without(video.Entries, "v.qq.com")
	edu := docGroup(&doc, "edu")
	edu.Entries = append(edu.Entries,
		categoryDocumentEntry{Value: "smoba.qq.com"},
		categoryDocumentEntry{Value: " Extra.EXAMPLE "},
		categoryDocumentEntry{Value: "*.wild.example"},
		categoryDocumentEntry{Name: "公共 DNS", Value: "8.8.8.8"},
	)
	view, err := svc.ImportCategories(ctx, mustJSON(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	got := entryValues(view.Categories)
	if got["game"]["smoba.qq.com"] || got["video"]["v.qq.com"] || got["game"]["9.9.9.9"] {
		t.Fatal("old entries should be gone")
	}
	if !got["music"]["kugou.com"] || !got["video"]["video.qq.com"] {
		t.Fatal("untouched builtin entries should stay")
	}
	for _, v := range []string{"smoba.qq.com", "extra.example", "*.wild.example", "8.8.8.8"} {
		if !got["edu"][v] {
			t.Fatalf("edu missing %s", v)
		}
	}
	m := live.Get()
	idx, ok := m.Match("smoba.qq.com")
	if !ok || m.Categories()[m.App(idx).Category].ID != "edu" {
		t.Fatal("moved domain did not take effect")
	}
	if _, ok := m.Match("v.qq.com"); ok {
		t.Fatal("removed domain still matches")
	}
	if _, ok := m.Match("a.wild.example"); !ok {
		t.Fatal("imported wildcard should match")
	}
	ipIdx, ok := m.MatchIP(netip.MustParseAddr("8.8.8.8"))
	if !ok {
		t.Fatal("imported ip should match")
	}
	if m.App(ipIdx).Name != "公共 DNS" {
		t.Fatalf("imported name = %s", m.App(ipIdx).Name)
	}
	var namedIP bool
	for _, e := range groupByID(view.Categories, "edu").Entries {
		if e.Value == "8.8.8.8" && e.Name == "公共 DNS" {
			namedIP = true
		}
	}
	if !namedIP {
		t.Fatal("imported ip name was dropped")
	}
	if _, ok := m.MatchIP(netip.MustParseAddr("9.9.9.9")); ok {
		t.Fatal("previous custom ip should be gone")
	}
}

func TestImportRejectsAndKeepsCurrent(t *testing.T) {
	svc, _ := openSettings(t)
	ctx := context.Background()
	if _, err := svc.AddRule(ctx, "game", "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	body, err := svc.ExportCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var doc categoryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}

	tooMany := cloneDoc(doc)
	game := docGroup(&tooMany, "game")
	for i := 0; i < maxAdded+1; i++ {
		game.Entries = append(game.Entries, categoryDocumentEntry{Value: "c" + itoa(i) + ".import.example"})
	}
	dup := cloneDoc(doc)
	dupGame := docGroup(&dup, "game")
	dupGame.Entries = append(append([]categoryDocumentEntry{}, dupGame.Entries...), categoryDocumentEntry{Value: "v.qq.com"})
	same := cloneDoc(doc)
	edu := docGroup(&same, "edu")
	edu.Entries = append(edu.Entries, categoryDocumentEntry{Value: "extra.example"}, categoryDocumentEntry{Value: "Extra.Example"})
	missing := cloneDoc(doc)
	missing.Categories = missing.Categories[:len(missing.Categories)-1]
	unknown := cloneDoc(doc)
	docGroup(&unknown, "other").ID = "nope"
	emptyVideo := cloneDoc(doc)
	docGroup(&emptyVideo, "video").Entries = nil

	cases := []struct {
		name string
		body []byte
		want string
	}{
		{name: "junk", body: []byte("nope"), want: "文件格式不正确"},
		{name: "array", body: []byte("[]"), want: "文件格式不正确"},
		{name: "extra field", body: bytes.Replace(body, []byte("{"), []byte(`{"note":1,`), 1), want: "文件格式不正确"},
		{name: "bad entry", body: bytes.Replace(body, []byte(`"entries": [`), []byte(`"entries": [ "bad",`), 1), want: "类别「游戏」的名单格式不正确"},
		{name: "null entries", body: mustJSON(t, emptyVideo), want: "类别「视频」的名单格式不正确"},
		{name: "bad domain", body: withValue(t, doc, "edu", "not a domain"), want: "域名格式不正确：not a domain"},
		{name: "bare host", body: withValue(t, doc, "edu", "localhost"), want: "域名格式不正确：localhost"},
		{name: "bad pattern", body: withValue(t, doc, "edu", "**.bad.com"), want: "域名格式不正确：**.bad.com"},
		{name: "bad ip", body: withValue(t, doc, "edu", "999.1.1.1"), want: "IP 地址格式不正确：999.1.1.1"},
		{name: "ipv6", body: withValue(t, doc, "edu", "2001:db8::1"), want: "IP 地址格式不正确：2001:db8::1"},
		{name: "leading zero", body: withValue(t, doc, "edu", "01.2.3.4"), want: "IP 地址格式不正确：01.2.3.4"},
		{name: "duplicate", body: mustJSON(t, dup), want: "v.qq.com 不能同时出现在「游戏」和「视频」"},
		{name: "repeat", body: mustJSON(t, same), want: "extra.example 在「学习教育」里重复了"},
		{name: "missing", body: mustJSON(t, missing), want: "缺少类别："},
		{name: "unknown", body: mustJSON(t, unknown), want: "没有这个类别：nope"},
		{name: "too many", body: mustJSON(t, tooMany), want: "添加的条目太多了"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ImportCategories(ctx, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
	view, err := svc.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !entryValues(view.Categories)["game"]["9.9.9.9"] {
		t.Fatal("failed import changed the current lists")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
