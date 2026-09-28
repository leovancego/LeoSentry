package category

import (
	"net/netip"
	"testing"
)

func TestCatalogAddIPAndRemoveDomain(t *testing.T) {
	_, m, err := Catalog(Edit{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Match("film.v.qq.com"); !ok {
		t.Fatal("builtin domain should match")
	}
	_, m, err = Catalog(Edit{Added: []Added{{Category: "game", Value: "1.2.3.4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.MatchIP(netip.MustParseAddr("1.2.3.4")); !ok {
		t.Fatal("added ip should match")
	}
	_, m, err = Catalog(Edit{Removed: []string{"v.qq.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Match("v.qq.com"); ok {
		t.Fatal("removed domain should not match")
	}
	if _, ok := m.Match("video.qq.com"); !ok {
		t.Fatal("other builtin domain should stay")
	}
}

func TestCatalogMoveBuiltinDomain(t *testing.T) {
	groups, m, err := Catalog(Edit{
		Removed: []string{"v.qq.com"},
		Added:   []Added{{Category: "edu", Value: "v.qq.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, ok := m.Match("v.qq.com")
	if !ok {
		t.Fatal("moved domain should still match")
	}
	cat := m.Categories()[m.App(idx).Category]
	if cat.ID != "edu" {
		t.Fatalf("category = %s", cat.ID)
	}
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Value == "v.qq.com" && g.ID != "edu" {
				t.Fatalf("v.qq.com still in %s", g.ID)
			}
		}
	}
}

func TestParseValue(t *testing.T) {
	kind, value, err := ParseValue(" Example.COM ")
	if err != nil || kind != KindDomain || value != "example.com" {
		t.Fatalf("domain = %s %s %v", kind, value, err)
	}
	kind, value, err = ParseValue("*.Example.com")
	if err != nil || kind != KindPattern || value != "*.example.com" {
		t.Fatalf("pattern = %s %s %v", kind, value, err)
	}
	kind, _, err = ParseValue("not a domain")
	if err == nil || kind != "" {
		t.Fatal("expected reject")
	}
}
